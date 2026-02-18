package cline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	BaseURL = "https://api.cline.bot"
)

type ClineAuth struct {
	client *http.Client
}

type TokenResponse struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	UserInfo     UserInfo `json:"userInfo"`
}

type UserInfo struct {
	Email       string `json:"email"`
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type AuthorizeResponse struct {
	URL   string `json:"url"`
	State string `json:"state"`
}

type APIResponse struct {
	Success bool          `json:"success"`
	Data    TokenResponse `json:"data"`
}

type tokenResponseWire struct {
	AccessToken  string          `json:"accessToken"`
	RefreshToken string          `json:"refreshToken"`
	ExpiresAt    json.RawMessage `json:"expiresAt"`
	UserInfo     UserInfo        `json:"userInfo"`
}

type apiResponseWire struct {
	Success bool              `json:"success"`
	Data    tokenResponseWire `json:"data"`
}

func NewClineAuth() *ClineAuth {
	return &ClineAuth{client: &http.Client{Timeout: 30 * time.Second}}
}

func (c *ClineAuth) InitiateOAuth(ctx context.Context, callbackURL string) (authURL string, state string, err error) {
	endpoint, err := url.Parse(BaseURL + "/api/v1/auth/authorize")
	if err != nil {
		return "", "", fmt.Errorf("cline: failed to build authorize URL: %w", err)
	}

	q := endpoint.Query()
	q.Set("callbackUrl", callbackURL)
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", "", fmt.Errorf("cline: failed to create authorize request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("cline: failed to call authorize endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("cline: failed to initiate oauth: status %d body %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data AuthorizeResponse
	if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", fmt.Errorf("cline: failed to decode authorize response: %w", err)
	}
	if data.URL == "" || data.State == "" {
		return "", "", fmt.Errorf("cline: failed to initiate oauth: missing url or state")
	}

	return data.URL, data.State, nil
}

func (c *ClineAuth) ExchangeCode(ctx context.Context, code, state string) (*TokenResponse, error) {
	payload := map[string]string{"code": code, "state": state}
	data, err := c.postAuthJSON(ctx, "/api/v1/auth/token", payload)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to exchange code: %w", err)
	}
	return data, nil
}

func (c *ClineAuth) RefreshTokens(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	payload := map[string]string{"refreshToken": refreshToken}
	data, err := c.postAuthJSON(ctx, "/api/v1/auth/refresh", payload)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to refresh tokens: %w", err)
	}
	return data, nil
}

func (c *ClineAuth) GetUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/api/v1/users/me", nil)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to create get user info request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer workos:"+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to call user info endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to read user info response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cline: failed to get user info: status %d body %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var wrapped struct {
		Success bool     `json:"success"`
		Data    UserInfo `json:"data"`
	}
	if err = json.Unmarshal(body, &wrapped); err == nil && wrapped.Data.Email != "" {
		return &wrapped.Data, nil
	}

	var direct UserInfo
	if err = json.Unmarshal(body, &direct); err != nil {
		return nil, fmt.Errorf("cline: failed to decode user info response: %w", err)
	}
	if direct.Email == "" {
		return nil, fmt.Errorf("cline: failed to decode user info response: missing email")
	}

	return &direct, nil
}

func (c *ClineAuth) StartCallbackServer(ctx context.Context, port int) (code string, state string, err error) {
	start := port
	if start < 48801 || start > 48811 {
		start = 48801
	}

	var listener net.Listener
	for p := start; p <= 48811; p++ {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			break
		}
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			continue
		}
	}
	if listener == nil {
		return "", "", fmt.Errorf("cline: failed to start callback server: no available ports in range 48801-48811")
	}

	resultCh := make(chan [2]string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		callbackCode := r.URL.Query().Get("code")
		callbackState := r.URL.Query().Get("state")
		if callbackCode == "" || callbackState == "" {
			http.Error(w, "missing code or state", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("cline: failed to parse callback parameters"):
			default:
			}
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Cline authentication completed. You can close this window."))

		select {
		case resultCh <- [2]string{callbackCode, callbackState}:
		default:
		}
	})

	server := &http.Server{Handler: mux}
	serverErrCh := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serverErrCh <- serveErr
		}
	}()

	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}

	select {
	case <-ctx.Done():
		shutdown()
		return "", "", fmt.Errorf("cline: callback server context canceled: %w", ctx.Err())
	case serverErr := <-serverErrCh:
		shutdown()
		return "", "", fmt.Errorf("cline: callback server failed: %w", serverErr)
	case callbackErr := <-errCh:
		shutdown()
		return "", "", callbackErr
	case result := <-resultCh:
		shutdown()
		return result[0], result[1], nil
	}
}

func (c *ClineAuth) postAuthJSON(ctx context.Context, path string, payload any) (*TokenResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to encode request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cline: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to call endpoint %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cline: endpoint %s returned status %d body %s", path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var apiResp apiResponseWire
	if err = json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("cline: failed to decode token response: %w", err)
	}
	if !apiResp.Success {
		return nil, fmt.Errorf("cline: endpoint %s returned unsuccessful response", path)
	}

	expiresAt, err := parseExpiresAt(apiResp.Data.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to parse expiresAt: %w", err)
	}

	return &TokenResponse{
		AccessToken:  apiResp.Data.AccessToken,
		RefreshToken: apiResp.Data.RefreshToken,
		ExpiresAt:    expiresAt,
		UserInfo:     apiResp.Data.UserInfo,
	}, nil
}

func parseExpiresAt(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("empty expiresAt")
	}

	var sec int64
	if err := json.Unmarshal(raw, &sec); err == nil {
		return sec, nil
	}

	var secFloat float64
	if err := json.Unmarshal(raw, &secFloat); err == nil {
		return int64(secFloat), nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsedInt, convErr := strconv.ParseInt(text, 10, 64); convErr == nil {
			return parsedInt, nil
		}
		if parsedTime, timeErr := time.Parse(time.RFC3339Nano, text); timeErr == nil {
			return parsedTime.Unix(), nil
		}
	}

	return 0, fmt.Errorf("unsupported expiresAt format")
}

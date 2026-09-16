// Package workbuddy implements authentication and token management for WorkBuddy,
// the global realm of the CodeBuddy/WorkBuddy family served from www.workbuddy.ai.
package workbuddy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	// BaseURL is the WorkBuddy global service boundary.
	BaseURL = "https://www.workbuddy.ai"
	// DefaultDomain is the WorkBuddy account domain sent in X-Domain.
	DefaultDomain = "www.workbuddy.ai"
	// UserAgent identifies chat/model requests.
	UserAgent = "WorkBuddy/5.5.4 WorkBuddy AI/5.5.4 CLI/2.137.1"
	// AuthUserAgent identifies the OAuth device-authorization requests.
	AuthUserAgent = "CLI/2.63.2 CodeBuddy/2.63.2"

	statePath   = "/v2/plugin/auth/state"
	tokenPath   = "/v2/plugin/auth/token"
	accountPath = "/v2/plugin/login/account"
	refreshPath = "/v2/plugin/auth/token/refresh"

	pollInterval     = 5 * time.Second
	maxPollDuration  = 5 * time.Minute
	codeLoginPending = 11217
	codeSuccess      = 0
)

// WorkBuddyAuth drives the WorkBuddy OAuth device authorization flow and token refresh.
type WorkBuddyAuth struct {
	httpClient *http.Client
	cfg        *config.Config
	baseURL    string
}

// NewWorkBuddyAuth constructs a WorkBuddy auth client.
func NewWorkBuddyAuth(cfg *config.Config) *WorkBuddyAuth {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		httpClient = util.SetProxy(&cfg.SDKConfig, httpClient)
	}
	return &WorkBuddyAuth{httpClient: httpClient, cfg: cfg, baseURL: BaseURL}
}

// AuthState holds the state and auth URL returned by the auth state API.
type AuthState struct {
	State   string
	AuthURL string
}

// AccountIdentity is the account lookup response used for request headers.
type AccountIdentity struct {
	UserID       string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}

// FetchAuthState calls POST /v2/plugin/auth/state?platform=CLI to get the state and login URL.
func (a *WorkBuddyAuth) FetchAuthState(ctx context.Context) (*AuthState, error) {
	stateURL := fmt.Sprintf("%s%s?platform=CLI", a.baseURL, statePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stateURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("workbuddy: failed to create auth state request: %w", err)
	}
	a.applyClientHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-Request-ID", uuid.NewString())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workbuddy: auth state request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("workbuddy auth state: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("workbuddy: failed to read auth state response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workbuddy: auth state request returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			State   string `json:"state"`
			AuthURL string `json:"authUrl"`
		} `json:"data"`
	}
	if err = json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("workbuddy: failed to parse auth state response: %w", err)
	}
	if result.Code != codeSuccess {
		return nil, fmt.Errorf("workbuddy: auth state request failed with code %d: %s", result.Code, result.Msg)
	}
	if result.Data == nil || result.Data.State == "" || result.Data.AuthURL == "" {
		return nil, fmt.Errorf("workbuddy: auth state response missing state or authUrl")
	}
	return &AuthState{State: result.Data.State, AuthURL: result.Data.AuthURL}, nil
}

type pollResponse struct {
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	RequestID string `json:"requestId"`
	Data      *struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		TokenType    string `json:"tokenType"`
		Domain       string `json:"domain"`
	} `json:"data"`
}

// PollForToken polls until the user completes browser authorization.
func (a *WorkBuddyAuth) PollForToken(ctx context.Context, state string) (*TokenStorage, error) {
	deadline := time.Now().Add(maxPollDuration)
	pollURL := fmt.Sprintf("%s%s?state=%s", a.baseURL, tokenPath, url.QueryEscape(state))

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTokenFetchFailed, err)
		}
		a.applyClientHeaders(req)
		req.Header.Set("X-No-Authorization", "true")
		req.Header.Set("X-No-User-Id", "true")

		resp, err := a.httpClient.Do(req)
		if err != nil {
			log.Debugf("workbuddy poll: request error: %v", err)
			continue
		}
		body, errRead := io.ReadAll(resp.Body)
		statusCode := resp.StatusCode
		_ = resp.Body.Close()
		if errRead != nil {
			log.Debugf("workbuddy poll: read error: %v", errRead)
			continue
		}
		if statusCode != http.StatusOK {
			log.Debugf("workbuddy poll: unexpected status %d", statusCode)
			continue
		}

		var result pollResponse
		if err = json.Unmarshal(body, &result); err != nil {
			continue
		}

		switch result.Code {
		case codeSuccess:
			if result.Data == nil {
				return nil, fmt.Errorf("%w: empty data in response", ErrTokenFetchFailed)
			}
			return &TokenStorage{
				AccessToken:  result.Data.AccessToken,
				RefreshToken: result.Data.RefreshToken,
				ExpiresAt:    time.Now().Add(time.Duration(result.Data.ExpiresIn) * time.Second).Unix(),
				TokenType:    result.Data.TokenType,
				Domain:       valueOrDefault(result.Data.Domain, DefaultDomain),
				Realm:        "global",
				Type:         "workbuddy",
			}, nil
		case codeLoginPending:
			// Continue polling until authorization completes or the deadline is reached.
		default:
			return nil, fmt.Errorf("%w: server returned code %d: %s", ErrTokenFetchFailed, result.Code, result.Msg)
		}
	}
	return nil, ErrPollingTimeout
}

// FetchAccountIdentity resolves the account identity after token acquisition.
func (a *WorkBuddyAuth) FetchAccountIdentity(ctx context.Context, state, accessToken string) (*AccountIdentity, error) {
	accountURL := fmt.Sprintf("%s%s?state=%s", a.baseURL, accountPath, url.QueryEscape(state))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAccountLookup, err)
	}
	a.applyClientHeaders(req)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: account request failed: %v", ErrAccountLookup, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("workbuddy account: close body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read account response: %v", ErrAccountLookup, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: account request returned status %d: %s", ErrAccountLookup, resp.StatusCode, string(body))
	}

	var result struct {
		Code int              `json:"code"`
		Msg  string           `json:"msg"`
		Data *AccountIdentity `json:"data"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("%w: failed to parse account response: %v", ErrAccountLookup, err)
	}
	if result.Code != codeSuccess {
		return nil, fmt.Errorf("%w: account request failed with code %d: %s", ErrAccountLookup, result.Code, result.Msg)
	}
	if result.Data == nil || result.Data.UserID == "" {
		return nil, fmt.Errorf("%w: account response missing uid", ErrAccountLookup)
	}
	return result.Data, nil
}

// RefreshToken exchanges a refresh token for a new access token.
func (a *WorkBuddyAuth) RefreshToken(ctx context.Context, accessToken, refreshToken, userID, domain string) (*TokenStorage, error) {
	if domain == "" {
		domain = DefaultDomain
	}
	refreshURL := fmt.Sprintf("%s%s", a.baseURL, refreshPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("workbuddy: failed to create refresh request: %w", err)
	}
	a.applyClientHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-Refresh-Token", refreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Request-ID", strings.ReplaceAll(uuid.NewString(), "-", ""))

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workbuddy: refresh request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("workbuddy refresh: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("workbuddy: failed to read refresh response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("workbuddy: refresh token rejected (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workbuddy: refresh failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			AccessToken      string `json:"accessToken"`
			RefreshToken     string `json:"refreshToken"`
			ExpiresIn        int64  `json:"expiresIn"`
			RefreshExpiresIn int64  `json:"refreshExpiresIn"`
			TokenType        string `json:"tokenType"`
			Domain           string `json:"domain"`
		} `json:"data"`
	}
	if err = json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("workbuddy: failed to parse refresh response: %w", err)
	}
	if result.Code != codeSuccess {
		return nil, fmt.Errorf("workbuddy: refresh failed with code %d: %s", result.Code, result.Msg)
	}
	if result.Data == nil {
		return nil, fmt.Errorf("workbuddy: empty data in refresh response")
	}

	newUserID := userID
	if newUserID == "" {
		newUserID, _ = a.DecodeUserID(result.Data.AccessToken)
	}
	return &TokenStorage{
		AccessToken:      result.Data.AccessToken,
		RefreshToken:     result.Data.RefreshToken,
		ExpiresAt:        time.Now().Add(time.Duration(result.Data.ExpiresIn) * time.Second).Unix(),
		RefreshExpiresIn: result.Data.RefreshExpiresIn,
		TokenType:        result.Data.TokenType,
		Domain:           valueOrDefault(result.Data.Domain, domain),
		Realm:            "global",
		UserID:           newUserID,
		Type:             "workbuddy",
	}, nil
}

// DecodeUserID decodes the sub claim from a JWT access token as a fallback identity.
func (a *WorkBuddyAuth) DecodeUserID(accessToken string) (string, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return "", ErrJWTDecodeFailed
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrJWTDecodeFailed, err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err = json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("%w: %v", ErrJWTDecodeFailed, err)
	}
	if claims.Sub == "" {
		return "", fmt.Errorf("%w: sub claim is empty", ErrJWTDecodeFailed)
	}
	return claims.Sub, nil
}

// applyClientHeaders sets the Client/Origin header family shared by auth requests.
func (a *WorkBuddyAuth) applyClientHeaders(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-CodeBuddy-Request", "1")
	req.Header.Set("Origin", BaseURL)
	req.Header.Set("Referer", BaseURL+"/")
	req.Header.Set("Accept-Language", "en-US")
	req.Header.Set("X-Product", "WorkBuddy")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("X-No-Department-Info", "true")
	req.Header.Set("X-Domain", DefaultDomain)
	req.Header.Set("User-Agent", AuthUserAgent)
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

package qoder

import (
	"bytes"
	"context"
	"crypto/sha256"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/denisbrodbeck/machineid"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

// DeviceFlowClient handles the Qoder device flow + PKCE authentication.
type DeviceFlowClient struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewDeviceFlowClient creates a new DeviceFlowClient.
func NewDeviceFlowClient(cfg *config.Config) *DeviceFlowClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &DeviceFlowClient{
		httpClient: client,
		cfg:        cfg,
	}
}

// centerURL returns the authorization center URL (used for browser redirect).
func (c *DeviceFlowClient) centerURL() string {
	if c.cfg != nil {
		if ep := c.cfg.GetOAuthEndpointOverride("qoder").DeviceAuthorizeURL; ep != "" {
			return ep
		}
	}
	return defaultCenterURL
}

// openapiURL returns the OpenAPI base URL (used for polling and token operations).
func (c *DeviceFlowClient) openapiURL() string {
	if c.cfg != nil {
		if ep := c.cfg.GetOAuthEndpointOverride("qoder").ApiBaseURL; ep != "" {
			return ep
		}
	}
	return defaultOpenapiURL
}

// pollEndpoint returns the poll URL for token retrieval.
func (c *DeviceFlowClient) pollEndpoint(nonce, verifier string) string {
	if c.cfg != nil {
		if ep := c.cfg.GetOAuthEndpointOverride("qoder").TokenURL; ep != "" {
			params := url.Values{}
			params.Set("nonce", nonce)
			params.Set("verifier", verifier)
			params.Set("challenge_method", "S256")
			return ep + "?" + params.Encode()
		}
	}
	params := url.Values{}
	params.Set("nonce", nonce)
	params.Set("verifier", verifier)
	params.Set("challenge_method", "S256")
	return c.openapiURL() + "/api/v1/deviceToken/poll?" + params.Encode()
}

// refreshEndpoint returns the token refresh URL.
func (c *DeviceFlowClient) refreshEndpoint() string {
	if c.cfg != nil {
		if ep := c.cfg.GetOAuthEndpointOverride("qoder").RefreshURL; ep != "" {
			return ep
		}
	}
	return c.openapiURL() + "/api/v1/deviceToken/refresh"
}

// patExchangeEndpoint returns the PAT exchange URL.
func (c *DeviceFlowClient) patExchangeEndpoint() string {
	return c.openapiURL() + "/api/v1/jobToken/exchange"
}

// generatePKCEVerifier creates a cryptographically random PKCE code verifier (86 base64url chars).
func generatePKCEVerifier() (string, error) {
	b := make([]byte, 64)
	if _, err := io.ReadFull(crand.Reader, b); err != nil {
		return "", fmt.Errorf("qoder: failed to generate PKCE verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// computePKCEChallenge computes the S256 PKCE challenge from a verifier.
func computePKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// getMachineID returns the machine ID, falling back to a random UUID on error.
func getMachineID() string {
	id, err := machineid.ID()
	if err != nil || id == "" {
		return uuid.New().String()
	}
	return id
}

// clientID returns the configured or default client ID.
func (c *DeviceFlowClient) clientID() string {
	// Client ID could be overridden via config metadata in the future.
	return qoderClientID
}

// StartDeviceFlow generates PKCE material and constructs the browser verification URL.
func (c *DeviceFlowClient) StartDeviceFlow(_ context.Context) (*QoderDeviceFlow, error) {
	verifier, err := generatePKCEVerifier()
	if err != nil {
		return nil, NewAuthenticationError(ErrDeviceFlowFailed, err)
	}

	challenge := computePKCEChallenge(verifier)
	nonce := uuid.New().String()
	machineID := getMachineID()

	params := url.Values{}
	params.Set("challenge", challenge)
	params.Set("challenge_method", "S256")
	params.Set("nonce", nonce)
	params.Set("machine_id", machineID)
	params.Set("client_id", c.clientID())

	verificationURI := c.centerURL() + "/device/selectAccounts?" + params.Encode()

	return &QoderDeviceFlow{
		Nonce:           nonce,
		Verifier:        verifier,
		Challenge:       challenge,
		MachineID:       machineID,
		VerificationURI: verificationURI,
		ExpiresIn:       defaultFlowExpiry,
	}, nil
}

// PollForToken polls the Qoder token endpoint until the user authorizes or the flow expires.
// A 404 response means authorization is pending; a 200 response carries the token.
func (c *DeviceFlowClient) PollForToken(ctx context.Context, flow *QoderDeviceFlow) (*QoderTokenData, error) {
	if flow == nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("device flow is nil"))
	}

	interval := time.Duration(defaultPollInterval) * time.Second
	deadline := time.Now().Add(time.Duration(maxPollDuration) * time.Second)
	if flow.ExpiresIn > 0 {
		flowDeadline := time.Now().Add(time.Duration(flow.ExpiresIn) * time.Second)
		if flowDeadline.Before(deadline) {
			deadline = flowDeadline
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, NewAuthenticationError(ErrPollingTimeout, ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, ErrPollingTimeout
			}

			token, pending, err := c.pollOnce(ctx, flow.Nonce, flow.Verifier)
			if err != nil {
				return nil, err
			}
			if pending {
				continue
			}
			return token, nil
		}
	}
}

// pollOnce makes a single poll request. Returns (token, pending, error).
// pending=true means the user has not yet authorized; caller should retry.
func (c *DeviceFlowClient) pollOnce(ctx context.Context, nonce, verifier string) (*QoderTokenData, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.pollEndpoint(nonce, verifier), nil)
	if err != nil {
		return nil, false, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "qoder/"+qoderVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder poll: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode == http.StatusNotFound {
		// Not ready yet — keep polling.
		return nil, true, nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return nil, false, ErrAccessDenied
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, NewAuthenticationError(ErrTokenExchangeFailed,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)))
	}

	var tokenResp QoderTokenResponse
	if err = json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return nil, false, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	accessToken := tokenResp.AccessToken()
	if accessToken == "" {
		return nil, false, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("empty access token in response"))
	}

	return normalizeTokenResponse(&tokenResp), false, nil
}

// RefreshToken exchanges a refresh token for a new access token.
func (c *DeviceFlowClient) RefreshToken(ctx context.Context, refreshToken string) (*QoderTokenData, error) {
	if refreshToken == "" {
		return nil, NewAuthenticationError(ErrRefreshFailed, fmt.Errorf("refresh token is empty"))
	}

	body, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return nil, NewAuthenticationError(ErrRefreshFailed, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.refreshEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, NewAuthenticationError(ErrRefreshFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "qoder/"+qoderVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewAuthenticationError(ErrRefreshFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder refresh: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewAuthenticationError(ErrRefreshFailed, err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, NewAuthenticationError(ErrRefreshFailed,
			fmt.Errorf("refresh token rejected (status %d)", resp.StatusCode))
	}

	if resp.StatusCode != http.StatusOK {
		return nil, NewAuthenticationError(ErrRefreshFailed,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)))
	}

	var tokenResp QoderTokenResponse
	if err = json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return nil, NewAuthenticationError(ErrRefreshFailed, err)
	}

	accessToken := tokenResp.AccessToken()
	if accessToken == "" {
		return nil, NewAuthenticationError(ErrRefreshFailed, fmt.Errorf("empty access token in refresh response"))
	}

	return normalizeTokenResponse(&tokenResp), nil
}

// ExchangePAT exchanges a personal access token for a session token.
func (c *DeviceFlowClient) ExchangePAT(ctx context.Context, personalToken string) (*QoderTokenData, error) {
	if personalToken == "" {
		return nil, NewAuthenticationError(ErrPATExchangeFailed, fmt.Errorf("personal token is empty"))
	}

	body, err := json.Marshal(map[string]string{"personal_token": personalToken})
	if err != nil {
		return nil, NewAuthenticationError(ErrPATExchangeFailed, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.patExchangeEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, NewAuthenticationError(ErrPATExchangeFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "qoder/"+qoderVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewAuthenticationError(ErrPATExchangeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder pat exchange: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewAuthenticationError(ErrPATExchangeFailed, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, NewAuthenticationError(ErrPATExchangeFailed,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)))
	}

	var tokenResp QoderTokenResponse
	if err = json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return nil, NewAuthenticationError(ErrPATExchangeFailed, err)
	}

	accessToken := tokenResp.AccessToken()
	if accessToken == "" {
		return nil, NewAuthenticationError(ErrPATExchangeFailed, fmt.Errorf("empty access token in PAT exchange response"))
	}

	return normalizeTokenResponse(&tokenResp), nil
}

// normalizeTokenResponse converts a QoderTokenResponse into QoderTokenData.
func normalizeTokenResponse(r *QoderTokenResponse) *QoderTokenData {
	expiresAt := r.ExpiresAt
	if expiresAt == "" && r.ExpireTime > 0 {
		expiresAt = time.Unix(r.ExpireTime, 0).UTC().Format(time.RFC3339)
	}
	return &QoderTokenData{
		UserID:                r.UserID,
		UserName:              r.UserName,
		AccessToken:           r.AccessToken(),
		RefreshToken:          r.RefreshToken,
		ExpiresAt:             expiresAt,
		RefreshTokenExpiresAt: r.RefreshTokenExpiresAt,
	}
}

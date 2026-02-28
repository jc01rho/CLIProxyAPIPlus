// Package cline provides authentication and token management functionality
// for Cline AI services using WorkOS OAuth.
package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// BaseURL is the base URL for the Cline API.
	BaseURL = "https://api.cline.bot"

	// AuthTimeout is the timeout for OAuth authentication flow.
	AuthTimeout = 10 * time.Minute
)

// TokenResponse represents the response from Cline token endpoints.
type TokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"` // Cline returns ISO 8601 timestamp string
	Email        string `json:"email"`
}

// ClineAuth provides methods for handling the Cline WorkOS authentication flow.
type ClineAuth struct {
	client *http.Client
	cfg    *config.Config
}

// NewClineAuth creates a new instance of ClineAuth.
func NewClineAuth(cfg *config.Config) *ClineAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	client.Timeout = 30 * time.Second
	return &ClineAuth{
		client: client,
		cfg:    cfg,
	}
}

// GenerateAuthURL generates the Cline OAuth authorization URL.
// The state parameter is used for CSRF protection.
func (c *ClineAuth) GenerateAuthURL(state, callbackURL string) string {
	// Cline uses WorkOS OAuth with the following parameters:
	// client_type=extension&callback_url={cb}&redirect_uri={cb}
	authURL := fmt.Sprintf("%s/api/v1/auth/authorize?client_type=extension&callback_url=%s&redirect_uri=%s&state=%s",
		BaseURL,
		callbackURL,
		callbackURL,
		state)
	return authURL
}

// ExchangeCode exchanges the authorization code for access and refresh tokens.
func (c *ClineAuth) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	payload := map[string]string{
		"grant_type":   "authorization_code",
		"code":         code,
		"redirect_uri": redirectURI,
		"client_type":  "extension",
		"provider":     "workos",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to marshal token request: %w", err)
	}

	tokenURL := BaseURL + "/api/v1/auth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("cline: failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Cline/3.0.0")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline: token request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("cline: token exchange failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("cline: token exchange failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("cline: failed to parse token response: %w", err)
	}

	return &tokenResp, nil
}

// RefreshToken refreshes an expired access token using the refresh token.
func (c *ClineAuth) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	payload := map[string]string{
		"grantType":    "refresh_token",
		"refreshToken": refreshToken,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to marshal refresh request: %w", err)
	}

	refreshURL := BaseURL + "/api/v1/auth/refresh"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("cline: failed to create refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Cline/3.0.0")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline: refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("cline: token refresh failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("cline: token refresh failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("cline: failed to parse refresh response: %w", err)
	}

	return &tokenResp, nil
}

// ShouldRefresh checks if the token should be refreshed (expires in less than 5 minutes).
func ShouldRefresh(expiresAt int64) bool {
	return time.Until(time.Unix(expiresAt, 0)) < 5*time.Minute
}

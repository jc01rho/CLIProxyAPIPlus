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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// BaseURL is the base URL for the Cline API.
	BaseURL = "https://api.cline.bot"

	// AuthTimeout is the timeout for OAuth authentication flow.
	AuthTimeout = 10 * time.Minute

	// TokenURL is the Cline OAuth token exchange endpoint.
	TokenURL = BaseURL + "/api/v1/auth/token"

	// RefreshURL is the Cline OAuth refresh endpoint.
	RefreshURL = BaseURL + "/api/v1/auth/refresh"

	// AuthorizeURL is the Cline OAuth authorize endpoint.
	AuthorizeURL = BaseURL + "/api/v1/auth/authorize"

	// UsersMeURL is the Cline user identity endpoint used for token validation.
	UsersMeURL = BaseURL + "/api/v1/users/me"

	// ClientType is the OAuth client type used for the extension flow.
	ClientType = "extension"

	// workosPrefix is the prefix Cline expects on access tokens in Bearer headers.
	workosPrefix = "workos:"
)

// TokenResponse represents the response from Cline token endpoints.
// Cline can return either a flat object or a wrapper `{success, data: {...}}`
// envelope. Both shapes are accepted; the wrapper's inner `data` object is
// flattened into the fields below, matching the 9Router contract.
type TokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"` // Cline returns ISO 8601 timestamp string (or Unix seconds)
	Email        string `json:"email"`
	// userInfo is only populated when the API returns the nested `data.userInfo` object.
	userInfo userInfo `json:"-"`
}

// userInfo is the optional user object nested under the wrapper `data` envelope.
type userInfo struct {
	Email string `json:"email"`
	ID    string `json:"id"`
}

// UnmarshalJSON accepts both flat and wrapper (`data`) token response shapes.
// It also flattens nested `data.userInfo` into the outer fields.
func (tr *TokenResponse) UnmarshalJSON(b []byte) error {
	// Try the flat shape first.
	var flat struct {
		AccessToken  string   `json:"accessToken"`
		RefreshToken string   `json:"refreshToken"`
		ExpiresAt    string   `json:"expiresAt"`
		Email        string   `json:"email"`
		UserInfo     userInfo `json:"userInfo"`
	}
	if err := json.Unmarshal(b, &flat); err != nil {
		return err
	}
	if flat.AccessToken != "" || flat.RefreshToken != "" || flat.Email != "" {
		tr.AccessToken = flat.AccessToken
		tr.RefreshToken = flat.RefreshToken
		tr.ExpiresAt = flat.ExpiresAt
		tr.Email = flat.Email
		tr.userInfo = flat.UserInfo
		return nil
	}

	// Fall back to the wrapper `{success, data}` shape.
	var wrapped struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken  string   `json:"accessToken"`
			RefreshToken string   `json:"refreshToken"`
			ExpiresAt    string   `json:"expiresAt"`
			Email        string   `json:"email"`
			UserInfo     userInfo `json:"userInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &wrapped); err != nil {
		return err
	}
	tr.AccessToken = wrapped.Data.AccessToken
	tr.RefreshToken = wrapped.Data.RefreshToken
	tr.ExpiresAt = wrapped.Data.ExpiresAt
	tr.Email = wrapped.Data.Email
	tr.userInfo = wrapped.Data.UserInfo
	return nil
}

// UserEmail returns the account email, preferring the nested `data.userInfo`
// email when the top-level email field is empty.
func (tr *TokenResponse) UserEmail() string {
	if strings.TrimSpace(tr.Email) != "" {
		return strings.TrimSpace(tr.Email)
	}
	return strings.TrimSpace(tr.userInfo.Email)
}

// ClineAuth provides methods for handling the Cline WorkOS authentication flow.
// The token/refresh/validate endpoints default to the exported constants but
// can be overridden per-instance (primarily for HTTP-contract tests) via the
// returned helpers. Production code should always use NewClineAuth.
type ClineAuth struct {
	client       *http.Client
	cfg          *config.Config
	tokenURL     string
	refreshURL   string
	usersMeURL   string
	authorizeURL string
}

// NewClineAuth creates a new instance of ClineAuth.
func NewClineAuth(cfg *config.Config) *ClineAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	client.Timeout = 30 * time.Second
	return &ClineAuth{
		client:       client,
		cfg:          cfg,
		tokenURL:     TokenURL,
		refreshURL:   RefreshURL,
		usersMeURL:   UsersMeURL,
		authorizeURL: AuthorizeURL,
	}
}

// WithEndpoints overrides the token/refresh/users-me/authorize endpoint URLs.
// It is intended for HTTP-contract tests that want to point the authenticator
// at an httptest server. Empty values fall back to the package constants.
func (c *ClineAuth) WithEndpoints(tokenURL, refreshURL, usersMeURL, authorizeURL string) *ClineAuth {
	if c == nil {
		return c
	}
	if tokenURL != "" {
		c.tokenURL = tokenURL
	}
	if refreshURL != "" {
		c.refreshURL = refreshURL
	}
	if usersMeURL != "" {
		c.usersMeURL = usersMeURL
	}
	if authorizeURL != "" {
		c.authorizeURL = authorizeURL
	}
	return c
}

func (c *ClineAuth) resolveTokenURL() string {
	if c != nil && c.tokenURL != "" {
		return c.tokenURL
	}
	return TokenURL
}

func (c *ClineAuth) resolveRefreshURL() string {
	if c != nil && c.refreshURL != "" {
		return c.refreshURL
	}
	return RefreshURL
}

func (c *ClineAuth) resolveUsersMeURL() string {
	if c != nil && c.usersMeURL != "" {
		return c.usersMeURL
	}
	return UsersMeURL
}

func (c *ClineAuth) resolveAuthorizeURL() string {
	if c != nil && c.authorizeURL != "" {
		return c.authorizeURL
	}
	return AuthorizeURL
}

// WithClient overrides the underlying http.Client. Intended for tests that
// need to use httptest.Server.Client().
func (c *ClineAuth) WithClient(client *http.Client) *ClineAuth {
	if c != nil && client != nil {
		c.client = client
	}
	return c
}

// GenerateAuthURL generates the Cline OAuth authorization URL.
// The state parameter is used for CSRF protection.
// The 9Router contract expects client_type=extension with both callback_url
// and redirect_uri set to the callback URL.
func (c *ClineAuth) GenerateAuthURL(state, callbackURL string) string {
	// Cline uses WorkOS OAuth with the following parameters:
	// client_type=extension&callback_url={cb}&redirect_uri={cb}
	authURL := fmt.Sprintf("%s?client_type=%s&callback_url=%s&redirect_uri=%s&state=%s",
		c.resolveAuthorizeURL(),
		ClientType,
		callbackURL,
		callbackURL,
		state)
	return authURL
}

// ExchangeCode exchanges the authorization code for access and refresh tokens.
// The code may be a base64-encoded token payload (returned directly in the
// callback) or a real authorization code requiring a server round-trip.
func (c *ClineAuth) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	// 9Router contract: {grant_type, code, client_type, redirect_uri} with no
	// provider key. The WorkOS binding is implied by the client_type=extension
	// flow and the token endpoint path.
	payload := map[string]string{
		"grant_type":   "authorization_code",
		"code":         code,
		"redirect_uri": redirectURI,
		"client_type":  ClientType,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.resolveTokenURL(), strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("cline: failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Cline/3.0.0")
	req.Header.Set("HTTP-Referer", "https://cline.bot")
	req.Header.Set("X-Title", "Cline")

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
// The 9Router refresh contract is `{refreshToken, grantType: "refresh_token",
// clientType: "extension"}`.
func (c *ClineAuth) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	payload := map[string]string{
		"grantType":    "refresh_token",
		"refreshToken": refreshToken,
		"clientType":   ClientType,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.resolveRefreshURL(), strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("cline: failed to create refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Cline/3.0.0")
	req.Header.Set("HTTP-Referer", "https://cline.bot")
	req.Header.Set("X-Title", "Cline")

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

// ValidateToken verifies a Cline access token against /api/v1/users/me.
// It returns the account email on success, or an error when the token is
// rejected or the endpoint returns a non-2xx status.
func (c *ClineAuth) ValidateToken(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolveUsersMeURL(), nil)
	if err != nil {
		return "", fmt.Errorf("cline: failed to create users/me request: %w", err)
	}

	req.Header.Set("Authorization", GetBearerHeaderValue(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Cline/3.0.0")
	req.Header.Set("HTTP-Referer", "https://cline.bot")
	req.Header.Set("X-Title", "Cline")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cline: users/me request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Debugf("cline: users/me validation failed (status %d)", resp.StatusCode)
		return "", fmt.Errorf("cline: users/me validation failed (status %d)", resp.StatusCode)
	}

	var identity struct {
		Email string `json:"email"`
		Data  *struct {
			Email string `json:"email"`
		} `json:"data"`
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("cline: failed to read users/me response: %w", err)
	}
	if err := json.Unmarshal(respBody, &identity); err != nil {
		return "", fmt.Errorf("cline: failed to parse users/me response: %w", err)
	}

	email := strings.TrimSpace(identity.Email)
	if email == "" && identity.Data != nil {
		email = strings.TrimSpace(identity.Data.Email)
	}
	return email, nil
}

// NormalizeAccessToken ensures the token carries the workos: prefix that the
// Cline API requires inside the Bearer scheme. Callers should pass tokens
// returned from the auth flow; this is a defensive helper for stored values.
func NormalizeAccessToken(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	if strings.HasPrefix(t, workosPrefix) {
		return t
	}
	return workosPrefix + t
}

// GetBearerHeaderValue builds the Authorization header value for a Cline token.
// The Cline API expects `Bearer workos:{token}`.
func GetBearerHeaderValue(token string) string {
	return "Bearer " + NormalizeAccessToken(token)
}

// WorkOSPrefix returns the workos: prefix expected on Cline access tokens.
func WorkOSPrefix() string {
	return workosPrefix
}

// ShouldRefresh checks if the token should be refreshed (expires in less than 5 minutes).
func ShouldRefresh(expiresAt int64) bool {
	if expiresAt <= 0 {
		return true
	}
	return time.Until(time.Unix(expiresAt, 0)) < 5*time.Minute
}

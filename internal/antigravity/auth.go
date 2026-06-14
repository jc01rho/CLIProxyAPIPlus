// Package antigravity implements Antigravity OAuth flow utilities and related helpers.
//
// This package provides:
//
//  1. Token refresh helpers: ParseRefreshParts, FormatRefreshParts (defined in project.go)
//  2. Expiry checks: AccessTokenExpired, CalculateTokenExpiry
//  3. Type guards: IsOAuthAuth
//  4. PKCE-based authorization URL building: BuildAuthURL
//
// The Antigravity OAuth flow uses Google OAuth with PKCE:
//   - Client ID: AntigravityClientID
//   - Scopes: AntigravityScopes (cloud-platform, userinfo, cclog, etc.)
//   - Refresh token format: "refreshToken|projectId|managedProjectId"
//
// Ported from antigravity-auth/packages/core/src/auth.ts and antigravity/oauth.ts.
package antigravity

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// accessTokenExpiryBufferMS is the buffer time (in ms) used to consider an access
// token expired before its actual expiry, to account for clock skew.
const accessTokenExpiryBufferMS = 60 * 1000

// AntigravityAuthURL is the Google OAuth authorization endpoint.
const AntigravityAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"

// AntigravityTokenURL is the Google OAuth token endpoint.
const AntigravityTokenURL = "https://oauth2.googleapis.com/token"

// AntigravityUserInfoURL is the Google userinfo endpoint.
const AntigravityUserInfoURL = "https://www.googleapis.com/oauth2/v1/userinfo?alt=json"

// IsOAuthAuth reports whether the value is a valid OAuth auth details struct.
func IsOAuthAuth(value any) bool {
	if value == nil {
		return false
	}
	if d, ok := value.(OAuthAuthDetails); ok {
		return d.Type == "oauth"
	}
	if m, ok := value.(map[string]any); ok {
		t, _ := m["type"].(string)
		return t == "oauth"
	}
	return false
}

// AccessTokenExpired reports whether the access token is expired or missing,
// with buffer for clock skew.
func AccessTokenExpired(auth OAuthAuthDetails) bool {
	if auth.Access == "" || auth.Expires <= 0 {
		return true
	}
	now := time.Now().UnixMilli()
	return auth.Expires <= now+accessTokenExpiryBufferMS
}

// CalculateTokenExpiry calculates absolute expiry timestamp based on a duration.
// requestTimeMs: the local time when the request was initiated.
// expiresInSeconds: the duration returned by the server (defaults to 3600s if invalid).
func CalculateTokenExpiry(requestTimeMs int64, expiresInSeconds any) int64 {
	var seconds float64
	switch v := expiresInSeconds.(type) {
	case int:
		seconds = float64(v)
	case int64:
		seconds = float64(v)
	case float64:
		seconds = v
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			seconds = f
		}
	default:
		seconds = 3600
	}
	if math.IsNaN(seconds) || seconds <= 0 {
		return requestTimeMs
	}
	return requestTimeMs + int64(seconds*1000)
}

// PKCEParams holds the generated PKCE verifier and challenge.
type PKCEParams struct {
	Verifier  string
	Challenge string
	State     string
}

// AuthState is the per-session auth flow state (state token, verifier, project).
type AuthState struct {
	State     string
	Verifier  string
	ProjectID string
	CreatedAt time.Time
}

// authStateStore keeps recent auth states by state token.
var (
	authStateStore   = make(map[string]AuthState)
	authStateStoreMu sync.RWMutex
	authStateTTL     = 10 * time.Minute
)

// StoreAuthState stores an auth state in the in-memory store.
func StoreAuthState(s AuthState) {
	if s.State == "" {
		return
	}
	authStateStoreMu.Lock()
	defer authStateStoreMu.Unlock()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	authStateStore[s.State] = s
	// Best-effort cleanup of expired entries
	cutoff := time.Now().Add(-authStateTTL)
	for k, v := range authStateStore {
		if v.CreatedAt.Before(cutoff) {
			delete(authStateStore, k)
		}
	}
}

// ConsumeAuthState returns the auth state and removes it from the store.
func ConsumeAuthState(state string) (AuthState, bool) {
	authStateStoreMu.Lock()
	defer authStateStoreMu.Unlock()
	s, ok := authStateStore[state]
	if !ok {
		return AuthState{}, false
	}
	delete(authStateStore, state)
	if !s.CreatedAt.IsZero() && time.Since(s.CreatedAt) > authStateTTL {
		return AuthState{}, false
	}
	return s, true
}

// BuildAuthURL builds the Google OAuth authorization URL with PKCE and state.
// codeChallenge should be the SHA-256 + base64url-encoded verifier.
func BuildAuthURL(state, codeChallenge string) string {
	q := url.Values{}
	q.Set("client_id", AntigravityClientID)
	q.Set("redirect_uri", AntigravityRedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(AntigravityScopes, " "))
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	return AntigravityAuthURL + "?" + q.Encode()
}

// PKCEVerifierLength is the byte length of the PKCE verifier (43 chars when base64url-encoded without padding).
const PKCEVerifierLength = 32

// StateTokenLength is the byte length of the state token.
const StateTokenLength = 16

// TokenResponse represents a Google OAuth token response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
}

// UserInfoResponse represents a Google userinfo response.
type UserInfoResponse struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	VerifiedEmail bool   `json:"verified_email"`
	Name          string `json:"name"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Picture       string `json:"picture"`
	Locale        string `json:"locale"`
}

// FormatStoredRefresh returns the stored refresh string from a TokenResponse.
// If the response has a refresh_token, it is used; otherwise the existing refresh is preserved.
func FormatStoredRefresh(existing, newRefreshToken string) string {
	if newRefreshToken != "" {
		parts := ParseRefreshParts(existing)
		parts.RefreshToken = newRefreshToken
		return FormatRefreshParts(parts)
	}
	return existing
}

// ShouldUpdateAccess reports whether the access token should be refreshed.
func ShouldUpdateAccess(auth OAuthAuthDetails) bool {
	return AccessTokenExpired(auth)
}

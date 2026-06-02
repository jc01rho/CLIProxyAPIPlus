// Package qoder provides authentication and token management functionality
// for Qoder services. It handles the device flow + PKCE token exchange,
// token storage, and refresh for maintaining authenticated sessions.
package qoder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// QoderTokenResponse is the raw API response from a successful token exchange or refresh.
// Field names reflect the Qoder API response format; alternative names are also captured.
type QoderTokenResponse struct {
	UserID                string `json:"user_id"`
	UserName              string `json:"user_name"`
	Token                 string `json:"token"`
	SecurityOAuthToken    string `json:"security_oauth_token"` // alternative field name
	RefreshToken          string `json:"refresh_token"`
	ExpiresAt             string `json:"expires_at,omitempty"`              // ISO 8601
	ExpireTime            int64  `json:"expire_time,omitempty"`             // Unix timestamp (seconds)
	RefreshTokenExpiresAt string `json:"refresh_token_expires_at,omitempty"` // ISO 8601
}

// AccessToken returns the effective access token, preferring Token over SecurityOAuthToken.
func (r *QoderTokenResponse) AccessToken() string {
	if r.Token != "" {
		return r.Token
	}
	return r.SecurityOAuthToken
}

// ExpiresAtTime returns the parsed expiry time from either the ISO 8601 or Unix timestamp field.
// Returns zero time if no expiry is available.
func (r *QoderTokenResponse) ExpiresAtTime() time.Time {
	if r.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, r.ExpiresAt); err == nil {
			return t
		}
	}
	if r.ExpireTime > 0 {
		return time.Unix(r.ExpireTime, 0)
	}
	return time.Time{}
}

// QoderTokenData holds the normalized token data after a successful authentication.
type QoderTokenData struct {
	// UserID is the Qoder user ID.
	UserID string
	// UserName is the Qoder username.
	UserName string
	// AccessToken is the bearer token for API requests.
	AccessToken string
	// RefreshToken is the token used to obtain a new access token.
	RefreshToken string
	// ExpiresAt is the RFC3339 timestamp when the access token expires.
	ExpiresAt string
	// RefreshTokenExpiresAt is the RFC3339 timestamp when the refresh token expires.
	RefreshTokenExpiresAt string
}

// QoderAuthBundle bundles authentication data for storage.
type QoderAuthBundle struct {
	TokenData *QoderTokenData
}

// QoderDeviceFlow holds the state for an in-progress Qoder device flow.
type QoderDeviceFlow struct {
	// Nonce is the UUID used as the session identifier for polling.
	Nonce string
	// Verifier is the PKCE code verifier (kept secret, sent during poll).
	Verifier string
	// Challenge is the PKCE code challenge (SHA-256 base64url, sent to browser).
	Challenge string
	// MachineID identifies the machine initiating the flow.
	MachineID string
	// VerificationURI is the full browser URL the user should visit.
	VerificationURI string
	// ExpiresIn is the number of seconds until the flow expires.
	ExpiresIn int
}

// QoderTokenStorage persists Qoder authentication tokens to disk.
type QoderTokenStorage struct {
	// UserID is the authenticated Qoder user ID.
	UserID string `json:"user_id"`
	// UserName is the Qoder username.
	UserName string `json:"user_name"`
	// AccessToken is the bearer token for API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain a new access token.
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresAt is the RFC3339 expiry of the access token.
	ExpiresAt string `json:"expires_at,omitempty"`
	// RefreshTokenExpiresAt is the RFC3339 expiry of the refresh token.
	RefreshTokenExpiresAt string `json:"refresh_token_expires_at,omitempty"`
	// Type is always "qoder" for this storage.
	Type string `json:"type"`
}

// SaveTokenToFile serializes the Qoder token storage to a JSON file.
func (ts *QoderTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "qoder"

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() { _ = f.Close() }()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// IsExpired returns true if the access token has expired (or expires within the next 60 seconds).
func (ts *QoderTokenStorage) IsExpired() bool {
	if ts.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, ts.ExpiresAt)
	if err != nil {
		return true
	}
	return time.Now().Add(60 * time.Second).After(t)
}

// NeedsRefresh returns true if the token should be refreshed (expired + has a refresh token).
func (ts *QoderTokenStorage) NeedsRefresh() bool {
	return ts.RefreshToken != "" && ts.IsExpired()
}

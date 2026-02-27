// Package cline provides authentication and token management functionality
// for Cline AI services.
package cline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	log "github.com/sirupsen/logrus"
)

// ClineTokenStorage stores token information for Cline authentication.
type ClineTokenStorage struct {
	// AccessToken is the Cline access token (stored without workos: prefix).
	AccessToken string `json:"accessToken"`

	// RefreshToken is the Cline refresh token.
	RefreshToken string `json:"refreshToken"`

	// ExpiresAt is the Unix timestamp when the access token expires.
	ExpiresAt int64 `json:"expiresAt"`

	// Email is the email address of the authenticated user.
	Email string `json:"email"`

	// Type indicates the authentication provider type, always "cline" for this storage.
	Type string `json:"type"`
}

// SaveTokenToFile serializes the Cline token storage to a JSON file.
func (ts *ClineTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "cline"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("failed to close file: %v", errClose)
		}
	}()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// LoadTokenFromFile loads a Cline token from a JSON file.
func LoadTokenFromFile(authFilePath string) (*ClineTokenStorage, error) {
	data, err := os.ReadFile(authFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read token file: %w", err)
	}

	var storage ClineTokenStorage
	if err := json.Unmarshal(data, &storage); err != nil {
		return nil, fmt.Errorf("failed to parse token file: %w", err)
	}

	return &storage, nil
}

// CredentialFileName returns the filename used to persist Cline credentials.
// Format: cline-{email}.json
func CredentialFileName(email string) string {
	return fmt.Sprintf("cline-%s.json", email)
}

// GetAuthHeaderValue returns the Authorization header value with workos: prefix.
// The token is stored without the prefix, but requests need it.
func (ts *ClineTokenStorage) GetAuthHeaderValue() string {
	return "workos:" + ts.AccessToken
}

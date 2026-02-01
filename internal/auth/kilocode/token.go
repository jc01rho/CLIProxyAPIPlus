// Package kilocode provides authentication and token management functionality
// for Kilocode AI services. It handles device flow token storage,
// serialization, and retrieval for maintaining authenticated sessions with the Kilocode API.
package kilocode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// KilocodeTokenStorage stores token information for Kilocode API authentication.
// It maintains compatibility with the existing auth system while adding Kilocode-specific fields
// for managing access tokens and user account information.
type KilocodeTokenStorage struct {
	// Token is the access token used for authenticating API requests.
	Token string `json:"token"`
	// UserID is the Kilocode user ID associated with this token.
	UserID string `json:"user_id"`
	// UserEmail is the Kilocode user email associated with this token.
	UserEmail string `json:"user_email"`
	// Type indicates the authentication provider type, always "kilocode" for this storage.
	Type string `json:"type"`
}

// KilocodeAuthBundle bundles authentication data for storage.
type KilocodeAuthBundle struct {
	// Token is the access token.
	Token string
	// UserID is the Kilocode user ID.
	UserID string
	// UserEmail is the Kilocode user email.
	UserEmail string
}

// SaveTokenToFile serializes the Kilocode token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *KilocodeTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "kilocode"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

package trae

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// TraeTokenBundle stores authentication bundle and state for Trae API.
// It implements the TokenStorage interface defined in internal/auth/models.go.
type TraeTokenBundle struct {
	// TraeAuthBundle is the raw JSON message containing authentication details.
	TraeAuthBundle *json.RawMessage `json:"trae_auth_bundle"`
	// State is the OAuth state string.
	State *string `json:"state"`
}

// SaveTokenToFile serializes the Trae token bundle to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
func (tb *TraeTokenBundle) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)

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

	if err = json.NewEncoder(f).Encode(tb); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// MarshalJSON implements the json.Marshaler interface for TraeTokenBundle.
func (tb *TraeTokenBundle) MarshalJSON() ([]byte, error) {
	type Alias TraeTokenBundle
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(tb),
	})
}

// UnmarshalJSON implements the json.Unmarshaler interface for TraeTokenBundle.
func (tb *TraeTokenBundle) UnmarshalJSON(data []byte) error {
	type Alias TraeTokenBundle
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(tb),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	return nil
}

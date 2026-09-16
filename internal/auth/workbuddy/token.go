package workbuddy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
)

// TokenStorage stores a WorkBuddy OAuth session plus the account identity that
// WorkBuddy request headers require.
type TokenStorage struct {
	AccessToken      string         `json:"access_token"`
	RefreshToken     string         `json:"refresh_token"`
	ExpiresAt        int64          `json:"expires_at"`
	RefreshExpiresIn int64          `json:"refresh_expires_in,omitempty"`
	TokenType        string         `json:"token_type,omitempty"`
	Domain           string         `json:"domain"`
	Realm            string         `json:"realm"`
	UserID           string         `json:"uid"`
	EnterpriseID     string         `json:"enterprise_id,omitempty"`
	Nickname         string         `json:"nickname,omitempty"`
	DeviceToken      string         `json:"device_token,omitempty"`
	Type             string         `json:"type"`
	Metadata         map[string]any `json:"-"`
}

// SetMetadata lets the file token store preserve runtime auth metadata on save.
func (s *TokenStorage) SetMetadata(metadata map[string]any) {
	s.Metadata = metadata
}

// SaveTokenToFile writes the WorkBuddy credential as a standalone auth file.
func (s *TokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	s.Type = "workbuddy"
	s.Realm = "global"
	if s.Domain == "" {
		s.Domain = DefaultDomain
	}
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("workbuddy: create credential directory: %w", err)
	}
	data, err := misc.MergeMetadata(s, s.Metadata)
	if err != nil {
		return fmt.Errorf("workbuddy: merge credential metadata: %w", err)
	}
	file, err := os.OpenFile(authFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("workbuddy: create credential file: %w", err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Errorf("workbuddy token storage: close credential file: %v", errClose)
		}
	}()
	if err = json.NewEncoder(file).Encode(data); err != nil {
		return fmt.Errorf("workbuddy: write credential file: %w", err)
	}
	return nil
}

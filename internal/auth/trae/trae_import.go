// Package trae provides token import functionality from existing Trae IDE installations.
// This module checks for existing Trae tokens in platform-specific locations and converts
// them to CLI Proxy's format for seamless migration.
package trae

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// traeIDEToken represents the token structure used by Trae IDE installations.
// This structure matches the format found in ~/.marscode/auth.json and similar locations.
type traeIDEToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Email        string `json:"email"`
	Expire       string `json:"expired,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"` // Alternative field name
	TokenType    string `json:"token_type,omitempty"`
}

// getTraeIDEPaths returns platform-specific paths where Trae IDE stores tokens.
// It checks multiple locations based on the operating system.
func getTraeIDEPaths() []string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Warnf("trae-import: failed to get home directory: %v", err)
		return nil
	}

	var paths []string

	switch runtime.GOOS {
	case "linux":
		// Linux: ~/.marscode/auth.json
		paths = append(paths,
			filepath.Join(homeDir, ".marscode", "auth.json"),
			filepath.Join(homeDir, ".config", "trae", "auth.json"),
		)

	case "darwin":
		// macOS: ~/Library/Application Support/Trae/
		paths = append(paths,
			filepath.Join(homeDir, "Library", "Application Support", "Trae", "auth.json"),
			filepath.Join(homeDir, ".marscode", "auth.json"),
			filepath.Join(homeDir, ".config", "trae", "auth.json"),
		)

	case "windows":
		// Windows: %APPDATA%/Trae/
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		paths = append(paths,
			filepath.Join(appData, "Trae", "auth.json"),
			filepath.Join(homeDir, ".marscode", "auth.json"),
		)

	default:
		// Fallback for unknown platforms
		paths = append(paths,
			filepath.Join(homeDir, ".marscode", "auth.json"),
		)
	}

	return paths
}

// findExistingTraeToken searches for existing Trae IDE token files.
// It returns the first valid token file found, or an error if none exist.
func findExistingTraeToken() (string, error) {
	paths := getTraeIDEPaths()
	if len(paths) == 0 {
		return "", fmt.Errorf("no valid paths to check for Trae tokens")
	}

	log.Debugf("trae-import: checking %d potential token locations", len(paths))

	for _, path := range paths {
		log.Debugf("trae-import: checking path: %s", path)

		if _, err := os.Stat(path); err == nil {
			log.Infof("trae-import: found existing token at: %s", path)
			return path, nil
		}
	}

	return "", fmt.Errorf("no existing Trae token found in any standard location")
}

// validateTraeToken performs basic validation on a Trae token.
// It checks for required fields and token format.
func validateTraeToken(token *traeIDEToken) error {
	if token.AccessToken == "" {
		return fmt.Errorf("access token is empty")
	}

	if token.Email == "" {
		return fmt.Errorf("email is empty")
	}

	// Check if token looks like a JWT (basic format check)
	parts := strings.Split(token.AccessToken, ".")
	if len(parts) != 3 && !strings.HasPrefix(token.AccessToken, traeJWTFormat) {
		log.Warnf("trae-import: token does not appear to be a valid JWT format")
	}

	// Check expiration if present
	expireTime := token.Expire
	if expireTime == "" {
		expireTime = token.ExpiresAt
	}

	if expireTime != "" {
		expTime, err := time.Parse(time.RFC3339, expireTime)
		if err != nil {
			log.Warnf("trae-import: failed to parse expiration time: %v", err)
		} else if time.Now().After(expTime) {
			return fmt.Errorf("token has expired at %s", expireTime)
		}
	}

	return nil
}

// loadTraeIDEToken reads and parses a Trae IDE token file.
func loadTraeIDEToken(path string) (*traeIDEToken, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read token file: %w", err)
	}

	var token traeIDEToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("failed to parse token JSON: %w", err)
	}

	return &token, nil
}

// convertToTraeAuthBundle converts a Trae IDE token to CLI Proxy's TraeAuthBundle format.
func convertToTraeAuthBundle(ideToken *traeIDEToken) *TraeAuthBundle {
	// Normalize expiration field
	expire := ideToken.Expire
	if expire == "" {
		expire = ideToken.ExpiresAt
	}

	// Ensure token has proper JWT format prefix
	accessToken := ideToken.AccessToken
	if !strings.HasPrefix(accessToken, traeJWTFormat) {
		accessToken = fmt.Sprintf("%s %s", traeJWTFormat, accessToken)
	}

	tokenData := TraeTokenData{
		AccessToken:  accessToken,
		RefreshToken: ideToken.RefreshToken,
		Email:        ideToken.Email,
		Expire:       expire,
	}

	bundle := &TraeAuthBundle{
		TokenData:   tokenData,
		LastRefresh: time.Now().Format(time.RFC3339),
	}

	return bundle
}

// ImportExistingTraeToken searches for and imports an existing Trae IDE token.
// It checks platform-specific paths, validates the token, and converts it to
// CLI Proxy's format. Returns nil if no token is found (not an error condition).
func (o *TraeAuth) ImportExistingTraeToken() (*TraeAuthBundle, error) {
	log.Info("trae-import: searching for existing Trae IDE token...")

	// Find token file
	tokenPath, err := findExistingTraeToken()
	if err != nil {
		log.Warnf("trae-import: %v", err)
		log.Info("trae-import: no existing token found - user will need to authenticate via OAuth")
		return nil, nil // Not an error - just no token to import
	}

	// Load token
	ideToken, err := loadTraeIDEToken(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load token from %s: %w", tokenPath, err)
	}

	// Validate token
	if err := validateTraeToken(ideToken); err != nil {
		log.Warnf("trae-import: token validation failed: %v", err)
		return nil, fmt.Errorf("invalid token in %s: %w", tokenPath, err)
	}

	// Convert to CLI Proxy format
	bundle := convertToTraeAuthBundle(ideToken)

	log.Infof("trae-import: successfully imported token for %s", ideToken.Email)
	log.Debugf("trae-import: token expires at: %s", bundle.TokenData.Expire)

	return bundle, nil
}

// GetImportedTokenEmail returns the email from an imported token file without full import.
// This is useful for checking if a token exists before attempting full import.
func GetImportedTokenEmail() (string, error) {
	tokenPath, err := findExistingTraeToken()
	if err != nil {
		return "", err
	}

	ideToken, err := loadTraeIDEToken(tokenPath)
	if err != nil {
		return "", err
	}

	return ideToken.Email, nil
}

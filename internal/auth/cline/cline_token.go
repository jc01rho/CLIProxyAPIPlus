// Package cline provides authentication and token management functionality
// for Cline AI services.
package cline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
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

	// UserID is the optional user identifier returned by /api/v1/users/me.
	UserID string `json:"userId,omitempty"`

	// Type indicates the authentication provider type, always "cline" for this storage.
	Type string `json:"type"`

	// metadata mirrors the auth.Metadata map persisted by the filestore. It is
	// populated via SetMetadata (called by the filestore before SaveTokenToFile)
	// and by SyncMetadata/ApplyMetadata helpers so the in-file JSON carries the
	// same keys the reloader reads back into coreauth.Auth.Metadata.
	metadata map[string]any
}

// SetMetadata stores the auth metadata map. It implements the private
// metadataSetter interface used by sdk/auth.FiletokenStore.Save so the token
// JSON file written to disk carries the same fields the reloader reads back
// into coreauth.Auth.Metadata (accessToken/refreshToken/expiresAt/email/userId
// in camelCase plus standard snake_case aliases).
func (ts *ClineTokenStorage) SetMetadata(md map[string]any) {
	ts.metadata = md
}

// SyncMetadata writes the current storage values into the supplied metadata
// map using both the standard snake_case keys and the Cline camelCase keys the
// executor and 9Router tooling read. It is the single source of truth for
// keeping ClineTokenStorage and coreauth.Auth.Metadata consistent.
func SyncMetadata(ts *ClineTokenStorage, md map[string]any) map[string]any {
	if md == nil {
		md = make(map[string]any)
	}
	if ts == nil {
		return md
	}
	md["type"] = "cline"
	md["access_token"] = ts.AccessToken
	md["accessToken"] = ts.AccessToken
	md["refresh_token"] = ts.RefreshToken
	md["refreshToken"] = ts.RefreshToken
	if ts.ExpiresAt > 0 {
		md["expires_at"] = ts.ExpiresAt
		md["expiresAt"] = ts.ExpiresAt
	}
	if strings.TrimSpace(ts.Email) != "" {
		md["email"] = ts.Email
	}
	if strings.TrimSpace(ts.UserID) != "" {
		md["user_id"] = ts.UserID
		md["userId"] = ts.UserID
	}
	return md
}

// ApplyMetadata copies token fields from the metadata map into the storage.
// It is used when an auth record is reloaded from disk (metadata-only) and the
// caller needs a concrete ClineTokenStorage for refresh/extraction.
func (ts *ClineTokenStorage) ApplyMetadata(md map[string]any) {
	if ts == nil || md == nil {
		return
	}
	if v := firstString(md, "accessToken", "access_token", "token"); v != "" {
		ts.AccessToken = strings.TrimPrefix(strings.TrimSpace(v), workosPrefix)
	}
	if v := firstString(md, "refreshToken", "refresh_token"); v != "" {
		ts.RefreshToken = strings.TrimSpace(v)
	}
	if v := firstInt64(md, "expiresAt", "expires_at", "expiredAt"); v > 0 {
		ts.ExpiresAt = v
	}
	if v := firstString(md, "email"); v != "" {
		ts.Email = strings.TrimSpace(v)
	}
	if v := firstString(md, "userId", "user_id"); v != "" {
		ts.UserID = strings.TrimSpace(v)
	}
	ts.Type = "cline"
}

func firstString(md map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := md[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstInt64(md map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch v := md[key].(type) {
		case int64:
			return v
		case int:
			return int64(v)
		case float64:
			return int64(v)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return n
			}
		case string:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// MetadataExpiry returns the token expiry timestamp from a metadata map,
// reading both the camelCase (expiresAt) and snake_case (expires_at) keys and
// accepting the numeric JSON types a disk reload can produce (int64, int,
// float64, json.Number, and numeric string). It returns 0 when no usable
// expiry is present.
func MetadataExpiry(md map[string]any) int64 {
	return firstInt64(md, "expiresAt", "expires_at", "expiredAt")
}

// SaveTokenToFile serializes the Cline token storage to a JSON file. When the
// filestore has called SetMetadata with a metadata map, the same map is merged
// into the JSON output so the reloader can rebuild a fully populated
// coreauth.Auth.Metadata record (camelCase + snake_case keys) without losing
// the in-memory storage fields.
func (ts *ClineTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "cline"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	payload := map[string]any{}
	if ts.metadata != nil {
		for k, v := range ts.metadata {
			payload[k] = v
		}
	}
	SyncMetadata(ts, payload)

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("failed to close file: %v", errClose)
		}
	}()

	enc := json.NewEncoder(f)
	if err := enc.Encode(payload); err != nil {
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

// ParseExpiresAt converts a Cline token response ExpiresAt value (which may be
// an ISO-8601 timestamp string, an integer/float Unix timestamp, or a numeric
// string) into a Unix timestamp in seconds. Returns 0 when the value cannot be
// parsed; callers should treat 0 as "unknown expiry".
func ParseExpiresAt(raw string) int64 {
	if raw == "" {
		return 0
	}
	// Try integer seconds first.
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n
	}
	// Try float seconds (e.g. "1700000000.123").
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return int64(f)
	}
	// Try ISO-8601 variants.
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Unix()
	}
	return 0
}

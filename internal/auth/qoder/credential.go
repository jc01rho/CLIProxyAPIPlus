// Package qoder provides credential file import for the Qoder CLI.
//
// The Qoder CLI stores its authenticated user credentials in a JSON file at
// either `~/.qoder/.auth/user` or `~/.qoderwork/.auth/user` (QoderWork builds).
// This file is created by `qoder login` / `qodercli login` and contains the
// OAuth access/refresh tokens together with user identity metadata.
//
// The CLIProxyAPIPlus Qoder authenticator previously only supported an
// interactive device-flow login. This loader allows reusing credentials that
// are already on disk so users do not have to re-authorize after they have
// logged in to the Qoder CLI once.
//
// The loader is intentionally permissive: the exact field names used by the
// Qoder CLI have evolved over time, so it accepts any of the historical
// spellings and falls back to deriving the access token from the
// `security_oauth_token` field when `access_token` / `token` are absent.
package qoder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Default credential file paths used by the Qoder CLI family.
//
// The "qoder" path is the production Qoder CLI. The "qoderwork" path is used
// by the QoderWork integration build. The latter is checked first because it
// is the newer naming convention.
var defaultCredentialPaths = []string{
	"~/.qoderwork/.auth/user",
	"~/.qoder/.auth/user",
}

// qoderCredentialFile is the on-disk representation of a Qoder CLI
// authenticated user. Every field is optional because different Qoder CLI
// builds emit slightly different JSON shapes; the loader only requires an
// access token (or a derivable equivalent) to succeed.
type qoderCredentialFile struct {
	// Identity fields.
	UserID   string `json:"user_id,omitempty"`
	UserName string `json:"user_name,omitempty"`
	UserID2  string `json:"userId,omitempty"`
	UserName2 string `json:"userName,omitempty"`
	Email    string `json:"email,omitempty"`

	// Access token candidates. The Qoder CLI historically used `token`
	// and `security_oauth_token`; newer builds (and PAT exchange responses)
	// also expose `access_token`.
	AccessToken          string `json:"access_token,omitempty"`
	Token                string `json:"token,omitempty"`
	SecurityOAuthToken   string `json:"security_oauth_token,omitempty"`

	// Refresh token candidates.
	RefreshToken string `json:"refresh_token,omitempty"`

	// Expiry candidates. `expires_at` is the canonical RFC3339 string;
	// `expire_time` / `expires_in` provide numeric alternatives.
	ExpiresAt             string `json:"expires_at,omitempty"`
	ExpireTime            int64  `json:"expire_time,omitempty"`
	ExpiresInSeconds      int64  `json:"expires_in,omitempty"`
	RefreshTokenExpiresAt string `json:"refresh_token_expires_at,omitempty"`

	// Type is preserved so downstream code can sanity-check the source.
	Type string `json:"type,omitempty"`
}

// extractAccessToken resolves the access token from the supported field names.
// The priority is `access_token` > `token` > `security_oauth_token`; the
// ordering is intentional because `security_oauth_token` is only populated
// by the PAT exchange path and `token` is the canonical Qoder CLI field.
func (c *qoderCredentialFile) extractAccessToken() string {
	if t := strings.TrimSpace(c.AccessToken); t != "" {
		return t
	}
	if t := strings.TrimSpace(c.Token); t != "" {
		return t
	}
	if t := strings.TrimSpace(c.SecurityOAuthToken); t != "" {
		return t
	}
	return ""
}

// effectiveUserID returns the user ID preferring user_id, then userId.
func (c *qoderCredentialFile) effectiveUserID() string {
	if v := strings.TrimSpace(c.UserID); v != "" {
		return v
	}
	return strings.TrimSpace(c.UserID2)
}

// effectiveUserName returns a user-friendly label preferring user_name,
// then userName, then email, then the user ID, and finally a placeholder.
func (c *qoderCredentialFile) effectiveUserName() string {
	for _, candidate := range []string{c.UserName, c.UserName2, c.Email} {
		if v := strings.TrimSpace(candidate); v != "" {
			return v
		}
	}
	if id := c.effectiveUserID(); id != "" {
		return id
	}
	return "qoder-user"
}

// normalizeExpiresAt returns the best-effort RFC3339 representation of the
// access token expiry. The function tries (in order) the explicit
// `expires_at` field, the numeric `expire_time` epoch, and a derived
// `expires_in` offset from the file's modification time. An empty string is
// returned when no expiry can be determined.
func (c *qoderCredentialFile) normalizeExpiresAt(modTime time.Time) string {
	if t := strings.TrimSpace(c.ExpiresAt); t != "" {
		// Trust the upstream value as-is to keep behaviour identical to
		// the OAuth flow loader. Callers parse this string in the same
		// place that parses the device-flow response.
		return t
	}
	if c.ExpireTime > 0 {
		return time.Unix(c.ExpireTime, 0).UTC().Format(time.RFC3339)
	}
	if c.ExpiresInSeconds > 0 && !modTime.IsZero() {
		return modTime.Add(time.Duration(c.ExpiresInSeconds) * time.Second).UTC().Format(time.RFC3339)
	}
	return ""
}

// LoadCredentialFile reads a Qoder CLI credential file from disk and returns
// the populated QoderTokenStorage. The path is expanded against the user's
// home directory when it starts with `~`.
func LoadCredentialFile(path string) (*QoderTokenStorage, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(expanded)
	if err != nil {
		return nil, fmt.Errorf("qoder: read credential file %q: %w", expanded, err)
	}

	// We need a mtime to derive expires_at from expires_in; it is not
	// required for the other code paths.
	var modTime time.Time
	if info, statErr := os.Stat(expanded); statErr == nil {
		modTime = info.ModTime()
	}

	storage, err := parseCredentialJSON(raw, modTime)
	if err != nil {
		// The Qoder CLI on some platforms (notably Windows builds older
		// than 6.6.x) stores its credential file as a `key=value` line
		// stream rather than a JSON object. Fall back to that format
		// when JSON parsing fails so users on those builds can still
		// reuse their existing login.
		storage, kvErr := parseCredentialKeyValue(raw, modTime)
		if kvErr != nil {
			return nil, err
		}
		if storage.AccessToken == "" {
			return nil, err
		}
		storage.Type = "qoder"
		return storage, nil
	}

	if storage.AccessToken == "" {
		return nil, fmt.Errorf("qoder: credential file %q does not contain an access token", expanded)
	}
	storage.Type = "qoder"
	return storage, nil
}

// LoadCredentialFromAnyDefault attempts to load a Qoder credential file from
// any of the well-known default locations. The first file that exists and
// parses successfully wins. The boolean return is true when a credential was
// found (even if loading failed for a parse error, which is also surfaced).
func LoadCredentialFromAnyDefault() (*QoderTokenStorage, string, error) {
	for _, p := range defaultCredentialPaths {
		expanded, err := expandHome(p)
		if err != nil {
			continue
		}
		if _, statErr := os.Stat(expanded); statErr != nil {
			continue
		}
		storage, loadErr := LoadCredentialFile(p)
		if loadErr != nil {
			return nil, expanded, loadErr
		}
		return storage, expanded, nil
	}
	return nil, "", fmt.Errorf("qoder: no credential file found in default locations (%s)",
		strings.Join(defaultCredentialPaths, ", "))
}

// parseCredentialJSON unmarshals the raw JSON into a QoderTokenStorage and
// applies all field-name fallbacks. modTime is used to derive expires_at
// from an expires_in offset when the file does not store a literal timestamp.
func parseCredentialJSON(raw []byte, modTime time.Time) (*QoderTokenStorage, error) {
	var file qoderCredentialFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("qoder: credential file is not valid JSON: %w", err)
	}

	storage := &QoderTokenStorage{
		UserID:                file.effectiveUserID(),
		UserName:              file.effectiveUserName(),
		AccessToken:           file.extractAccessToken(),
		RefreshToken:          strings.TrimSpace(file.RefreshToken),
		ExpiresAt:             file.normalizeExpiresAt(modTime),
		RefreshTokenExpiresAt: strings.TrimSpace(file.RefreshTokenExpiresAt),
	}

	if storage.AccessToken == "" {
		return nil, fmt.Errorf("qoder: credential file is missing access_token/token/security_oauth_token")
	}
	return storage, nil
}

// parseCredentialKeyValue parses a `key=value` line-based credential file.
// This is used by some Qoder CLI builds (notably Windows 6.6.x and older)
// where the credential file is a simple text file with one key=value pair
// per line, optionally including [section] headers that are ignored.
func parseCredentialKeyValue(raw []byte, modTime time.Time) (*QoderTokenStorage, error) {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	fields := make(map[string]string, 16)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key != "" {
			// Later lines override earlier ones for the same key.
			fields[strings.ToLower(key)] = val
		}
	}

	storage := &QoderTokenStorage{
		UserID:                firstNonEmpty(fields["user_id"], fields["userid"], fields["user"]),
		UserName:              firstNonEmpty(fields["user_name"], fields["username"], fields["name"], fields["email"]),
		AccessToken:           firstNonEmpty(fields["access_token"], fields["token"], fields["security_oauth_token"]),
		RefreshToken:          fields["refresh_token"],
		RefreshTokenExpiresAt: fields["refresh_token_expires_at"],
	}

	if v := strings.TrimSpace(fields["expires_at"]); v != "" {
		storage.ExpiresAt = v
	} else if v := strings.TrimSpace(fields["expire_time"]); v != "" {
		if epoch, convErr := strconv.ParseInt(v, 10, 64); convErr == nil && epoch > 0 {
			storage.ExpiresAt = time.Unix(epoch, 0).UTC().Format(time.RFC3339)
		}
	} else if v := strings.TrimSpace(fields["expires_in"]); v != "" {
		if secs, convErr := strconv.ParseInt(v, 10, 64); convErr == nil && secs > 0 && !modTime.IsZero() {
			storage.ExpiresAt = modTime.Add(time.Duration(secs) * time.Second).UTC().Format(time.RFC3339)
		}
	}

	if storage.AccessToken == "" {
		return nil, fmt.Errorf("qoder: credential file (key=value format) is missing access_token/token/security_oauth_token")
	}
	return storage, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return ""
}

// expandHome resolves a leading "~" or "~/" in path against the current
// user's home directory. Absolute paths are returned unchanged. The function
// never errors on missing HOME; in that case it returns the original path
// and lets the subsequent read fail with a normal file-not-found error.
func expandHome(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("qoder: credential path is empty")
	}
	if trimmed == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, trimmed[2:]), nil
	}
	return trimmed, nil
}

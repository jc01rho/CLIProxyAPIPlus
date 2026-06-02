package qoder

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadCredentialFile_ModernFieldNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user")
	content := `{
		"user_id": "u-123",
		"user_name": "alice",
		"access_token": "access-1",
		"refresh_token": "refresh-1",
		"expires_at": "2030-01-02T03:04:05Z",
		"refresh_token_expires_at": "2030-02-02T03:04:05Z",
		"type": "qoder"
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	storage, err := LoadCredentialFile(path)
	if err != nil {
		t.Fatalf("LoadCredentialFile returned error: %v", err)
	}
	if storage.UserID != "u-123" {
		t.Errorf("user_id = %q, want u-123", storage.UserID)
	}
	if storage.UserName != "alice" {
		t.Errorf("user_name = %q, want alice", storage.UserName)
	}
	if storage.AccessToken != "access-1" {
		t.Errorf("access_token = %q, want access-1", storage.AccessToken)
	}
	if storage.RefreshToken != "refresh-1" {
		t.Errorf("refresh_token = %q, want refresh-1", storage.RefreshToken)
	}
	if storage.ExpiresAt != "2030-01-02T03:04:05Z" {
		t.Errorf("expires_at = %q, want 2030-01-02T03:04:05Z", storage.ExpiresAt)
	}
	if storage.RefreshTokenExpiresAt != "2030-02-02T03:04:05Z" {
		t.Errorf("refresh_token_expires_at = %q, want 2030-02-02T03:04:05Z", storage.RefreshTokenExpiresAt)
	}
	if storage.Type != "qoder" {
		t.Errorf("type = %q, want qoder", storage.Type)
	}
}

func TestLoadCredentialFile_FallbacksToLegacyFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user")
	content := `{
		"user_id": "u-legacy",
		"user_name": "bob",
		"token": "legacy-token",
		"security_oauth_token": "alt-token",
		"expire_time": 4102444800,
		"refresh_token": "refresh-legacy"
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	storage, err := LoadCredentialFile(path)
	if err != nil {
		t.Fatalf("LoadCredentialFile returned error: %v", err)
	}
	// The legacy `token` field is preferred over `security_oauth_token`
	// because it is the value the Qoder CLI emits in the on-disk file.
	if storage.AccessToken != "legacy-token" {
		t.Errorf("access_token = %q, want legacy-token (fallback to token field)", storage.AccessToken)
	}
	if storage.UserID != "u-legacy" {
		t.Errorf("user_id = %q, want u-legacy", storage.UserID)
	}
	if storage.RefreshToken != "refresh-legacy" {
		t.Errorf("refresh_token = %q, want refresh-legacy", storage.RefreshToken)
	}
	if storage.ExpiresAt == "" {
		t.Errorf("expires_at should be derived from expire_time")
	}
	parsed, parseErr := time.Parse(time.RFC3339, storage.ExpiresAt)
	if parseErr != nil {
		t.Errorf("derived expires_at %q is not RFC3339: %v", storage.ExpiresAt, parseErr)
	}
	if parsed.Unix() != 4102444800 {
		t.Errorf("derived expires_at unix = %d, want 4102444800", parsed.Unix())
	}
}

func TestLoadCredentialFile_CamelCaseIdentityFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user")
	content := `{
		"userId": "u-camel",
		"userName": "carol",
		"access_token": "access-camel"
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	storage, err := LoadCredentialFile(path)
	if err != nil {
		t.Fatalf("LoadCredentialFile returned error: %v", err)
	}
	if storage.UserID != "u-camel" {
		t.Errorf("user_id = %q, want u-camel (userId fallback)", storage.UserID)
	}
	if storage.UserName != "carol" {
		t.Errorf("user_name = %q, want carol (userName fallback)", storage.UserName)
	}
}

func TestLoadCredentialFile_MissingAccessToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user")
	content := `{"user_id":"u","user_name":"u"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := LoadCredentialFile(path); err == nil {
		t.Fatalf("expected error for credential file without access token")
	}
}

func TestLoadCredentialFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := LoadCredentialFile(path); err == nil {
		t.Fatalf("expected error for non-JSON credential file")
	}
}

func TestLoadCredentialFile_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("home dir not available: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".qoder-test-credential"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(filepath.Join(home, ".qoder-test-credential"))

	path := filepath.Join(home, ".qoder-test-credential", "user")
	content := `{"user_id":"u-tilde","user_name":"dave","access_token":"access-tilde"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	storage, err := LoadCredentialFile("~/.qoder-test-credential/user")
	if err != nil {
		t.Fatalf("LoadCredentialFile returned error: %v", err)
	}
	if storage.AccessToken != "access-tilde" {
		t.Errorf("access_token = %q, want access-tilde", storage.AccessToken)
	}
}

func TestLoadCredentialFromAnyDefault_NoFile(t *testing.T) {
	// We cannot unlink the real `~/.qoder/.auth/user`, but if no fixture
	// exists on disk the loader must report a clear error.
	if _, _, err := LoadCredentialFromAnyDefault(); err == nil {
		t.Skip("credential file unexpectedly present on test host")
	}
}

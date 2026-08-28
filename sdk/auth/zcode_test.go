package auth

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
)

// TestZcodeCreateAuthRecord verifies the auth record maps zcode credentials
// to the api_key/base_url attributes the executor consumes.
func TestZcodeCreateAuthRecord(t *testing.T) {
	a := NewZcodeAuthenticator()
	creds := &zcode.Credentials{
		AccessToken:  "key-1.secret-abc",
		RefreshToken: "upstream-token",
		ExpiresAt:    time.Now().Add(10 * 365 * 24 * time.Hour),
		Email:        "user@example.com",
		AccountID:    "acct-123",
	}
	record, err := a.createAuthRecord(creds)
	if err != nil {
		t.Fatalf("createAuthRecord: %v", err)
	}
	if record.Provider != "zcode" {
		t.Errorf("Provider = %q, want zcode", record.Provider)
	}
	if record.Attributes["api_key"] != "key-1.secret-abc" {
		t.Errorf("api_key = %q, want key-1.secret-abc", record.Attributes["api_key"])
	}
	if record.Attributes["base_url"] != zcode.DefaultAnthropicBase {
		t.Errorf("base_url = %q, want %q", record.Attributes["base_url"], zcode.DefaultAnthropicBase)
	}
	if record.Metadata["refresh_token"] != "upstream-token" {
		t.Errorf("refresh_token = %v, want upstream-token", record.Metadata["refresh_token"])
	}
	if record.Attributes["email"] != "user@example.com" {
		t.Errorf("email = %q, want user@example.com", record.Attributes["email"])
	}
}

// TestSanitizeZcodeIdentifier verifies email sanitization for filenames.
func TestSanitizeZcodeIdentifier(t *testing.T) {
	if got := sanitizeZcodeIdentifier("User@Example.com"); got != "user-example.com" {
		t.Errorf("sanitize = %q, want user-example.com", got)
	}
	if got := sanitizeZcodeIdentifier("a/b/c"); got != "a-b-c" {
		t.Errorf("sanitize = %q, want a-b-c", got)
	}
}

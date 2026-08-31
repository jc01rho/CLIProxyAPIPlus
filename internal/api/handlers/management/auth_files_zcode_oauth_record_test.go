package management

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// buildZcodeOAuthRecord mirrors the auth record the web OAuth completion path
// builds in startZcodeOAuthFlow. Keeping it in one place lets the regression
// test assert the persisted fields without driving the whole polling loop.
func buildZcodeOAuthRecord(creds *zcode.Credentials, fileName string, now time.Time) *coreauth.Auth {
	return &coreauth.Auth{
		ID:        fileName,
		Provider:  "zcode",
		FileName:  fileName,
		Label:     "zcode",
		Status:    coreauth.StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata: map[string]any{
			"type":          "zcode",
			"access_token":  creds.AccessToken,
			"refresh_token": creds.RefreshToken,
			"expires_at":    creds.ExpiresAt.Format(time.RFC3339),
			"email":         creds.Email,
			"account_id":    creds.AccountID,
			"zcode_token":   creds.ZcodeToken,
		},
		Attributes: map[string]string{
			"api_key":     creds.AccessToken,
			"base_url":    zcode.DefaultAnthropicBase,
			"email":       creds.Email,
			"source":      "zcode-oauth",
			"zcode_token": creds.ZcodeToken,
		},
		NextRefreshAfter: creds.ExpiresAt.Add(-24 * time.Hour),
	}
}

// The web OAuth path builds its auth record independently of the CLI
// authenticator in sdk/auth/zcode.go, and it previously dropped the broker JWT
// entirely. The management plan-balance probe authenticates with that JWT, so a
// credential created from the management UI cannot report its balance without
// it. Both paths must persist the same fields.
func TestZcodeOAuthRecordPersistsBrokerToken(t *testing.T) {
	creds := &zcode.Credentials{
		AccessToken:  "key-1.secret",
		RefreshToken: "upstream-zai-token",
		ExpiresAt:    time.Now().Add(24 * time.Hour),
		Email:        "user@example.com",
		AccountID:    "acct-1",
		ZcodeToken:   "jwt-broker-1",
	}

	record := buildZcodeOAuthRecord(creds, "zcode-user-00001.json", time.Now())

	if got, _ := record.Metadata["zcode_token"].(string); got != "jwt-broker-1" {
		t.Fatalf("metadata zcode_token = %q, want jwt-broker-1", got)
	}
	if got := record.Attributes["zcode_token"]; got != "jwt-broker-1" {
		t.Fatalf("attributes zcode_token = %q, want jwt-broker-1", got)
	}
}

// Inference always authenticates with the provisioned Z.AI API key against
// api.z.ai. The zcode.z.ai start-plan gateway is not reachable from this proxy:
// it answers model requests with {"code":3007,"msg":"captcha verify failed"}
// because it requires the desktop app's client attestation. The record must not
// point anywhere else.
func TestZcodeOAuthRecordPinsProvisionedKeyToAnthropicBase(t *testing.T) {
	creds := &zcode.Credentials{
		AccessToken: "key-1.secret",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
		ZcodeToken:  "jwt-broker-1",
	}

	record := buildZcodeOAuthRecord(creds, "zcode-user-00001.json", time.Now())

	if got := record.Attributes["base_url"]; got != zcode.DefaultAnthropicBase {
		t.Fatalf("attributes base_url = %q, want %q", got, zcode.DefaultAnthropicBase)
	}
	if got := record.Attributes["api_key"]; got != "key-1.secret" {
		t.Fatalf("attributes api_key = %q, want the provisioned Z.AI key", got)
	}
}

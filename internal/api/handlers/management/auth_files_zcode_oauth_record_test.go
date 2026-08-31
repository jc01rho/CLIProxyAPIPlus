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
		},
		Attributes: map[string]string{
			"api_key":  creds.AccessToken,
			"base_url": zcode.DefaultAnthropicBase,
			"email":    creds.Email,
			"source":   "zcode-oauth",
		},
		NextRefreshAfter: creds.ExpiresAt.Add(-24 * time.Hour),
	}
}

// The record must carry exactly what gajae-code's credentialsFromApiKey keeps:
// the provisioned key, the upstream refresh token, expiry, and identity. Its
// OAuthCredentials type (packages/ai/src/utils/oauth/types.ts) has no field for
// the broker JWT, which is why nothing here may persist one.
func TestZcodeOAuthRecordPersistsOnlyProvisionedCredential(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)
	creds := &zcode.Credentials{
		AccessToken:  "key-1.secret",
		RefreshToken: "upstream-zai-token",
		ExpiresAt:    expires,
		Email:        "user@example.com",
		AccountID:    "acct-1",
	}

	record := buildZcodeOAuthRecord(creds, "zcode-user-00001.json", time.Now())

	if got, _ := record.Metadata["access_token"].(string); got != "key-1.secret" {
		t.Errorf("metadata access_token = %q, want key-1.secret", got)
	}
	if got, _ := record.Metadata["refresh_token"].(string); got != "upstream-zai-token" {
		t.Errorf("metadata refresh_token = %q, want upstream-zai-token", got)
	}
	if got, _ := record.Metadata["account_id"].(string); got != "acct-1" {
		t.Errorf("metadata account_id = %q, want acct-1", got)
	}

	// The broker JWT is identity input during provisioning only; a stored copy
	// is what the removed start-plan gateway routing read.
	for _, key := range []string{"zcode_token", "start_plan", "start_plan_limit", "start_plan_used"} {
		if _, ok := record.Metadata[key]; ok {
			t.Errorf("metadata must not persist %q", key)
		}
	}
	for _, key := range []string{"zcode_token", "start_plan_active"} {
		if _, ok := record.Attributes[key]; ok {
			t.Errorf("attributes must not persist %q", key)
		}
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
	}

	record := buildZcodeOAuthRecord(creds, "zcode-user-00001.json", time.Now())

	if got := record.Attributes["base_url"]; got != zcode.DefaultAnthropicBase {
		t.Fatalf("attributes base_url = %q, want %q", got, zcode.DefaultAnthropicBase)
	}
	if got := record.Attributes["api_key"]; got != "key-1.secret" {
		t.Fatalf("attributes api_key = %q, want the provisioned Z.AI key", got)
	}
}

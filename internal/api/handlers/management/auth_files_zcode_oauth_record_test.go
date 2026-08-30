package management

import (
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// buildZcodeOAuthRecord mirrors the auth record the web OAuth completion path
// builds in startZcodeOAuthFlow. Keeping it in one place lets the regression
// test assert the start-plan fields without driving the whole polling loop.
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
			"type":             "zcode",
			"access_token":     creds.AccessToken,
			"refresh_token":    creds.RefreshToken,
			"expires_at":       creds.ExpiresAt.Format(time.RFC3339),
			"email":            creds.Email,
			"account_id":       creds.AccountID,
			"zcode_token":      creds.ZcodeToken,
			"start_plan":       creds.StartPlanActive,
			"start_plan_limit": creds.StartPlanLimit,
			"start_plan_used":  creds.StartPlanUsed,
		},
		Attributes: map[string]string{
			"api_key":           creds.AccessToken,
			"base_url":          zcode.DefaultAnthropicBase,
			"email":             creds.Email,
			"source":            "zcode-oauth",
			"zcode_token":       creds.ZcodeToken,
			"start_plan_active": strconv.FormatBool(creds.StartPlanActive),
		},
		NextRefreshAfter: creds.ExpiresAt.Add(-24 * time.Hour),
	}
}

// The web OAuth path builds its auth record independently of the CLI
// authenticator in sdk/auth/zcode.go. It previously dropped the broker JWT and
// the start-plan flags, so a credential created from the management UI always
// fell back to api.z.ai and billed the individual plan instead of the start
// plan. Persisting these fields is what lets the executor route through the
// zcode.z.ai start-plan gateway.
func TestZcodeOAuthRecordPersistsStartPlanFields(t *testing.T) {
	creds := &zcode.Credentials{
		AccessToken:     "keyid.keysecret",
		RefreshToken:    "upstream-zai-oauth",
		ExpiresAt:       time.Now().Add(72 * time.Hour),
		Email:           "user@example.com",
		AccountID:       "acct-1",
		ZcodeToken:      "broker-jwt",
		StartPlanActive: true,
		StartPlanLimit:  8000000,
		StartPlanUsed:   1250000,
	}

	record := buildZcodeOAuthRecord(creds, "zcode-user-00001.json", time.Now())

	if got, _ := record.Metadata["zcode_token"].(string); got != "broker-jwt" {
		t.Fatalf("metadata zcode_token = %q, want %q", got, "broker-jwt")
	}
	if got, _ := record.Metadata["start_plan"].(bool); !got {
		t.Fatal("metadata start_plan = false, want true")
	}
	if got, _ := record.Metadata["start_plan_limit"].(int64); got != 8000000 {
		t.Fatalf("metadata start_plan_limit = %d, want 8000000", got)
	}
	if got, _ := record.Metadata["start_plan_used"].(int64); got != 1250000 {
		t.Fatalf("metadata start_plan_used = %d, want 1250000", got)
	}
	if got := record.Attributes["zcode_token"]; got != "broker-jwt" {
		t.Fatalf("attributes zcode_token = %q, want %q", got, "broker-jwt")
	}
	if got := record.Attributes["start_plan_active"]; got != "true" {
		t.Fatalf("attributes start_plan_active = %q, want \"true\"", got)
	}
}

// An account without an active start plan must not advertise one, so the
// executor keeps billing the provisioned Z.AI API key against api.z.ai.
func TestZcodeOAuthRecordWithoutStartPlan(t *testing.T) {
	creds := &zcode.Credentials{
		AccessToken:     "keyid.keysecret",
		RefreshToken:    "upstream-zai-oauth",
		ExpiresAt:       time.Now().Add(72 * time.Hour),
		Email:           "paid@example.com",
		ZcodeToken:      "broker-jwt",
		StartPlanActive: false,
	}

	record := buildZcodeOAuthRecord(creds, "zcode-paid-00002.json", time.Now())

	if got, _ := record.Metadata["start_plan"].(bool); got {
		t.Fatal("metadata start_plan = true, want false")
	}
	if got := record.Attributes["start_plan_active"]; got != "false" {
		t.Fatalf("attributes start_plan_active = %q, want \"false\"", got)
	}
}

package executor

import (
	"context"
	"fmt"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClaudeExecutorPrepareRequestAuthPopulatesCredentialIdentity(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(_ context.Context, _ *cliproxyauth.Auth, accessToken string) (*claudeauth.OAuthProfile, error) {
		if accessToken != "sk-ant-oat-prepare" {
			t.Fatalf("access token = %q, want selected credential token", accessToken)
		}
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		profile.Account.Email = "user@example.com"
		profile.Organization.UUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		profile.Organization.Name = "Example Org"
		return profile, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-old-credential",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat-prepare",
		},
		Metadata: map[string]any{"type": "claude"},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing credential identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	deviceIDs := claudeauth.NormalizeDeviceIDPool(prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	if len(deviceIDs) != claudeauth.ClaudeDevicePoolSize {
		t.Fatalf("device pool length = %d, want %d", len(deviceIDs), claudeauth.ClaudeDevicePoolSize)
	}
	if got := prepared.Metadata["account_uuid"]; got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account_uuid = %#v, want upstream profile account", got)
	}
	if got := prepared.Metadata["organization_uuid"]; got != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("organization_uuid = %#v, want upstream profile organization", got)
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuthMigratesFiveDevicesToOne(t *testing.T) {
	legacy := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444444444444444444444444444",
	}
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		t.Fatal("profile lookup should not run when account UUID is already present")
		return nil, nil
	}
	auth := &cliproxyauth.Auth{
		ID:         "claude-five-device-credential",
		Attributes: map[string]string{"api_key": "sk-ant-oat-five-device"},
		Metadata: map[string]any{
			"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			claudeauth.ClaudeDeviceIDsMetadataKey: legacy,
		},
	}
	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for legacy five-device pool")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	deviceIDs, ok := prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey].([]string)
	if !ok || len(deviceIDs) != 1 || deviceIDs[0] != legacy[0] {
		t.Fatalf("prepared device IDs = %#v, want first legacy device only", prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after single-device migration")
	}
}

func TestClaudeExecutorPrepareRequestAuthIgnoresFreshTimestampWithoutIdentity(t *testing.T) {
	calls := 0
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		calls++
		return nil, fmt.Errorf("profile unavailable")
	}
	const previousCheckedAt = "2999-01-01T00:00:00Z"
	auth := &cliproxyauth.Auth{
		ID:         "claude-profile-unavailable",
		Attributes: map[string]string{"api_key": "sk-ant-oat-profile-unavailable"},
		Metadata: map[string]any{
			"type":                                "claude",
			claudeAccountProfileCheckedAtKey:      previousCheckedAt,
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{"0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}

	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare == nil {
		t.Fatal("PrepareRequestAuth() error = nil, want missing account identity failure")
	}
	if prepared != nil {
		t.Fatalf("PrepareRequestAuth() auth = %#v, want nil on missing account identity", prepared)
	}
	if calls != 1 {
		t.Fatalf("profile calls = %d, want 1", calls)
	}
	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false after failed profile lookup; failure must remain retryable")
	}
	if got := claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileCheckedAtKey); got != previousCheckedAt {
		t.Fatalf("profile checked timestamp = %q, want prior value preserved without suppressing retry", got)
	}
}

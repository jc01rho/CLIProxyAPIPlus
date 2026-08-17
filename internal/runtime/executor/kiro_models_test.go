package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFetchKiroModelsReturnsNilWithoutCredential(t *testing.T) {
	if got := FetchKiroModels(context.Background(), nil, &config.Config{}); got != nil {
		t.Fatalf("FetchKiroModels(nil auth) = %d models, want nil", len(got))
	}
	if got := FetchKiroModels(context.Background(), &cliproxyauth.Auth{Metadata: map[string]any{}}, nil); got != nil {
		t.Fatalf("FetchKiroModels(nil cfg) = %d models, want nil", len(got))
	}
}

func TestFetchKiroModelsSkipsRuntimeEndpoint(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token": "tok",
		"api_host":     "https://runtime.us-east-1.kiro.dev",
	}}
	if got := FetchKiroModels(context.Background(), auth, &config.Config{}); got != nil {
		t.Fatalf("runtime.kiro.dev must skip ListAvailableModels, got %d models", len(got))
	}
}

func TestFetchKiroModelsSkipsSocialAuthWithoutHost(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token": "tok",
		"auth_method":  "google",
		"profile_arn":  "arn:aws:codewhisperer:us-east-1:123:profile/abc",
	}}
	if !isKiroRuntimeEndpoint(auth) {
		t.Fatal("google social kiro auth must skip ListAvailableModels")
	}
	if got := FetchKiroModels(context.Background(), auth, &config.Config{}); got != nil {
		t.Fatalf("social kiro auth must skip ListAvailableModels, got %d models", len(got))
	}
}

func Test_isKiroRuntimeEndpoint(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want bool
	}{
		{
			name: "runtime host metadata",
			meta: map[string]any{"api_host": "https://runtime.us-east-1.kiro.dev"},
			want: true,
		},
		{
			name: "google social without host",
			meta: map[string]any{
				"access_token": "tok",
				"auth_method":  "google",
				"profile_arn":  "arn:aws:codewhisperer:us-east-1:123:profile/abc",
			},
			want: true,
		},
		{
			name: "github social without host",
			meta: map[string]any{
				"access_token": "tok",
				"auth_method":  "github",
				"profile_arn":  "arn:aws:codewhisperer:us-east-1:123:profile/abc",
			},
			want: true,
		},
		{
			name: "idc with profile",
			meta: map[string]any{
				"access_token": "tok",
				"auth_method":  "idc",
				"profile_arn":  "arn:aws:codewhisperer:us-east-1:123:profile/abc",
			},
			want: true,
		},
		{
			name: "camelCase authMethod google",
			meta: map[string]any{
				"accessToken": "tok",
				"authMethod":  "google",
				"profileArn":  "arn:aws:codewhisperer:us-east-1:123:profile/abc",
			},
			want: true,
		},
		{
			name: "builder-id without profile",
			meta: map[string]any{
				"access_token":  "tok",
				"auth_method":   "builder-id",
				"client_id":     "cid",
				"client_secret": "sec",
			},
			want: false,
		},
		{
			name: "builder-id leftover profile still lists",
			meta: map[string]any{
				"access_token": "tok",
				"auth_method":  "builder-id",
				"profile_arn":  "arn:aws:codewhisperer:us-east-1:123:profile/abc",
			},
			want: false,
		},
		{
			name: "aws_sso_oidc without profile is builder-id",
			meta: map[string]any{
				"access_token":  "tok",
				"auth_type":     "aws_sso_oidc",
				"client_id":     "cid",
				"client_secret": "sec",
			},
			want: false,
		},
		{
			name: "aws_sso_oidc with profile is idc runtime",
			meta: map[string]any{
				"access_token": "tok",
				"auth_type":    "aws_sso_oidc",
				"profile_arn":  "arn:aws:codewhisperer:us-east-1:123:profile/abc",
			},
			want: true,
		},
		{
			name: "client credentials without method or profile is builder-id",
			meta: map[string]any{
				"access_token":  "tok",
				"client_id":     "cid",
				"client_secret": "sec",
			},
			want: false,
		},
		{
			name: "desktop token only without host",
			meta: map[string]any{"access_token": "tok"},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Metadata: tt.meta}
			if got := isKiroRuntimeEndpoint(auth); got != tt.want {
				t.Fatalf("isKiroRuntimeEndpoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKiroExecutorRejectsUnsupportedAliases(t *testing.T) {
	if reason := kirocommon.RejectKiroRequestedModel("auto-kiro"); reason != "" {
		t.Fatalf("auto-kiro must map, not reject: %s", reason)
	}
	if reason := kirocommon.RejectKiroRequestedModel("kiro-auto"); reason != "" {
		t.Fatalf("kiro-auto must stay executable: %s", reason)
	}
	if reason := kirocommon.RejectKiroRequestedModel("claude-sonnet-4.5[1m]"); reason != "" {
		t.Fatalf("[1m] must strip, not reject: %s", reason)
	}
	if reason := kirocommon.RejectKiroRequestedModel("claude-sonnet-4.5-thinking"); reason == "" {
		t.Fatal("expected non-adaptive -thinking rejection")
	}
}

func TestMapModelToKiroOmniRouteIDs(t *testing.T) {
	e := NewKiroExecutor(&config.Config{})
	cases := map[string]string{
		"kiro-claude-sonnet-5":          "claude-sonnet-5",
		"kiro-claude-sonnet-5-thinking": "claude-sonnet-5",
		"kiro-minimax-m2-5":             "minimax-m2.5",
		"kiro-glm-5":                    "glm-5",
		"kiro-gpt-5-6-sol":              "gpt-5.6-sol",
		"gpt-5.6-terra":                 "gpt-5.6-terra",
		"kiro-gpt-5-6-luna":             "gpt-5.6-luna",
		"kiro-claude-sonnet-5-agentic":  "claude-sonnet-5",
		"auto-kiro":                     "auto",
		"kiro-auto":                     "auto",
		"kiro-claude-opus-4-7":          "claude-opus-4.7",
		"claude-opus-4.8":               "claude-opus-4.8",
		"kiro-claude-opus-5":            "claude-opus-5",
		"claude-sonnet-5[1m]":           "claude-sonnet-5",
		"claude-sonnet-4.5[1m]":         "claude-sonnet-4.5",
	}
	for in, want := range cases {
		if got := e.mapModelToKiro(in); got != want {
			t.Errorf("mapModelToKiro(%q)=%q, want %q", in, got, want)
		}
	}
}

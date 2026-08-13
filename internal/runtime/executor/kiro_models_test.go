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

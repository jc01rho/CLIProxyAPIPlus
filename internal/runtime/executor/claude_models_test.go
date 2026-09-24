package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func claudeAuthWithBase(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "claude",
		Attributes: map[string]string{"api_key": "sk-test", "base_url": baseURL},
	}
}

func TestClaudeModelsEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://gw.example.com":           "https://gw.example.com/v1/models",
		"https://gw.example.com/":          "https://gw.example.com/v1/models",
		"https://gw.example.com/v1":        "https://gw.example.com/v1/models",
		"https://gw.example.com/v1/models": "https://gw.example.com/v1/models",
		"https://gw.example.com/anthropic": "https://gw.example.com/anthropic/v1/models",
		"  ":                               "",
	}
	for input, want := range cases {
		if got := claudeModelsEndpoint(input); got != want {
			t.Errorf("claudeModelsEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}

// Anthropic-hosted credentials must not pay a network round trip: the static
// catalog already describes that lineup.
func TestFetchClaudeModels_AnthropicBaseSkipsFetch(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, base := range []string{"", "https://api.anthropic.com"} {
		models := FetchClaudeModels(context.Background(), claudeAuthWithBase(base), nil)
		if len(models) == 0 {
			t.Fatalf("base %q: expected static fallback models", base)
		}
		if called {
			t.Fatalf("base %q: unexpected upstream call", base)
		}
	}
}

// The motivating case: a credential pointed at a third-party gateway lists the
// catalog that gateway actually serves, not Anthropic's static lineup.
func TestFetchClaudeModels_CustomGatewayCatalog(t *testing.T) {
	var gotPath, gotAuth, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gateway-sonnet","display_name":"Gateway Sonnet"},{"id":"gateway-haiku"}]}`))
	}))
	defer srv.Close()

	models := FetchClaudeModels(context.Background(), claudeAuthWithBase(srv.URL), nil)
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", gotVersion)
	}
	if len(models) != 2 {
		t.Fatalf("models = %d, want 2 (gateway catalog replaces the static list)", len(models))
	}
	if models[0].ID != "gateway-sonnet" || models[0].DisplayName != "Gateway Sonnet" {
		t.Fatalf("first model = %+v", models[0])
	}
	// A missing display_name falls back to the id.
	if models[1].ID != "gateway-haiku" || models[1].DisplayName != "gateway-haiku" {
		t.Fatalf("second model = %+v", models[1])
	}
}

func TestFetchClaudeModels_FailureFallsBackToStatic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	models := FetchClaudeModels(context.Background(), claudeAuthWithBase(srv.URL), nil)
	if len(models) == 0 {
		t.Fatal("expected static fallback on upstream failure")
	}
	for _, m := range models {
		if m.ID == "gateway-sonnet" {
			t.Fatal("gateway models leaked into the fallback")
		}
	}
}

// Gateway listings carry only ids and names. Ids that match Anthropic's own
// lineup must keep the static reasoning metadata, or clients see no thinking
// levels for models that do accept a thinking budget.
func TestFetchClaudeModels_GatewayKeepsStaticThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5","display_name":"Claude Opus 5"},{"id":"gateway-only"}]}`))
	}))
	defer srv.Close()

	models := FetchClaudeModels(context.Background(), claudeAuthWithBase(srv.URL), nil)
	byID := map[string]*registry.ModelInfo{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if byID["claude-opus-5"] == nil || byID["claude-opus-5"].Thinking == nil {
		t.Fatalf("claude-opus-5 lost static thinking: %+v", byID["claude-opus-5"])
	}
	if byID["claude-opus-5"].DisplayName != "Claude Opus 5" {
		t.Fatalf("display name = %q", byID["claude-opus-5"].DisplayName)
	}
	if byID["gateway-only"] == nil || byID["gateway-only"].Thinking != nil {
		t.Fatalf("gateway-only must not invent thinking: %+v", byID["gateway-only"])
	}
}

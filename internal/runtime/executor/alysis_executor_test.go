package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAlysisCredentialsPrefersMetadata(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{"gatewayKey": "slk_meta"},
		Attributes: map[string]string{"gatewayKey": "slk_attr"},
	}
	if got := alysisCredentials(auth); got != "slk_meta" {
		t.Fatalf("expected metadata key to win, got %q", got)
	}
}

func TestAlysisCredentialsFallsBackToAttributes(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"gatewayKey": "slk_attr"}}
	if got := alysisCredentials(auth); got != "slk_attr" {
		t.Fatalf("expected attribute key, got %q", got)
	}
	if got := alysisCredentials(nil); got != "" {
		t.Fatalf("expected empty key for nil auth, got %q", got)
	}
}

func TestAlysisPrepareRequestSetsBearer(t *testing.T) {
	e := NewAlysisExecutor(&config.Config{})
	req, _ := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
	if err := e.PrepareRequest(req, &cliproxyauth.Auth{
		Metadata: map[string]any{"gatewayKey": "slk_prepare"},
	}); err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer slk_prepare" {
		t.Fatalf("unexpected Authorization header %q", got)
	}
}

func TestAlysisPrepareRequestDropsEmptyAuth(t *testing.T) {
	e := NewAlysisExecutor(&config.Config{})
	req, _ := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
	req.Header.Set("Authorization", "Bearer stale")
	if err := e.PrepareRequest(req, &cliproxyauth.Auth{}); err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("expected Authorization cleared, got %q", got)
	}
}

func TestFetchAlysisModelsLiveAndFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected models path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer slk_models" {
			t.Errorf("unexpected Authorization %q", got)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-v4-flash","object":"model","owned_by":"alysis"},{"id":"future-model","object":"model","owned_by":"alysis"}]}`))
	}))
	defer server.Close()

	prevBase := alysisGatewayBase
	alysisGatewayBase = server.URL
	defer func() { alysisGatewayBase = prevBase }()

	auth := &cliproxyauth.Auth{Metadata: map[string]any{"gatewayKey": "slk_models"}}
	models := FetchAlysisModels(context.Background(), auth, &config.Config{})
	ids := make(map[string]bool, len(models))
	for _, m := range models {
		ids[m.ID] = true
	}
	if !ids["deepseek-v4-flash"] || !ids["future-model"] {
		t.Fatalf("expected live models merged with fallback, got %v", ids)
	}

	// Failure path: unreachable server falls back to the static catalog.
	alysisGatewayBase = "http://127.0.0.1:1"
	fallback := FetchAlysisModels(context.Background(), auth, &config.Config{})
	if len(fallback) == 0 || !strings.HasPrefix(fallback[0].ID, "deepseek") {
		t.Fatalf("expected static fallback catalog, got %d models", len(fallback))
	}
}

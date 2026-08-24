package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFetchKiloModelsUsesLiveCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("X-KiloCode-EditorName"); got != "CLIProxyAPIPlus" {
			t.Errorf("X-KiloCode-EditorName = %q, want CLIProxyAPIPlus", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer anonymous" {
			t.Errorf("Authorization = %q, want Bearer anonymous", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [{
				"id": "openrouter/new-live-model",
				"name": "New live model",
				"context_length": 1048576,
				"max_tokens": 32768
			}]
		}`))
	}))
	t.Cleanup(server.Close)

	models := fetchKiloModels(context.Background(), nil, &config.Config{}, server.URL, "kilo", registry.GetKiloModels())
	var found bool
	for _, model := range models {
		if model.ID != "openrouter/new-live-model" {
			continue
		}
		found = true
		if model.DisplayName != "New live model" {
			t.Errorf("display name = %q, want New live model", model.DisplayName)
		}
		if model.OwnedBy != "kilo" || model.Type != "kilo" {
			t.Errorf("ownership = %q/%q, want kilo/kilo", model.OwnedBy, model.Type)
		}
	}
	if !found {
		t.Fatalf("live model missing from merged catalog")
	}
}

func TestFetchKiloGatewayModelsUsesOptionalAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty (optional auth)", got)
		}
		if got := r.Header.Get("X-KiloCode-EditorName"); got != "CLIProxyAPIPlus" {
			t.Errorf("X-KiloCode-EditorName = %q, want CLIProxyAPIPlus", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "kilo-auto/frontier", "name": "Live frontier"},
				{"id": "kilo-auto/balanced", "name": "Live balanced"}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	models := fetchKiloModels(context.Background(), nil, &config.Config{}, server.URL, "kilo-gateway", registry.GetKiloGatewayModels())
	if len(models) < 2 {
		t.Fatalf("fetched models = %d, want >=2", len(models))
	}
}

func TestFetchKiloGatewayModelsFiltersAnonymousCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Anonymous gateway calls send no Authorization; credentialed calls send
		// the real token. Both hit the same catalog.
		if got := r.Header.Get("Authorization"); got != "" && got != "Bearer real-token" {
			t.Errorf("Authorization = %q, want empty or Bearer real-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "kilo-auto/frontier", "name": "Auto Frontier"},
				{"id": "kilo-auto/free", "name": "Auto Free"},
				{"id": "deepseek/deepseek-v4-pro", "name": "DeepSeek V4 Pro"},
				{"id": "qwen/qwen3-coder:free", "name": "Qwen3 Coder (free)"}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	anonymous := NewKiloGatewayAnonymousAuth()
	models := fetchKiloGatewayModels(context.Background(), anonymous, &config.Config{}, server.URL)
	for _, model := range models {
		if model.ID == "kilo-auto/frontier" || model.ID == "deepseek/deepseek-v4-pro" {
			t.Errorf("anonymous gateway catalog contains paid model %q", model.ID)
		}
	}
	hasFree := false
	for _, model := range models {
		if model.ID == "qwen/qwen3-coder:free" {
			hasFree = true
		}
	}
	if !hasFree {
		t.Fatalf("anonymous gateway catalog missing free model qwen/qwen3-coder:free")
	}
	if !KiloGatewayAuthIsAnonymous(anonymous) {
		t.Fatalf("KiloGatewayAuthIsAnonymous(synthesized) = false, want true")
	}

	credentialed := &cliproxyauth.Auth{
		ID:       "kilo-gateway-real",
		Provider: "kilo-gateway",
		Metadata: map[string]any{"kilocodeToken": "real-token"},
	}
	if KiloGatewayAuthIsAnonymous(credentialed) {
		t.Fatalf("KiloGatewayAuthIsAnonymous(credentialed) = true, want false")
	}
	full := fetchKiloGatewayModels(context.Background(), credentialed, &config.Config{}, server.URL)
	hasPaid := false
	for _, model := range full {
		if model.ID == "kilo-auto/frontier" {
			hasPaid = true
		}
	}
	if !hasPaid {
		t.Fatalf("credentialed gateway catalog missing paid model kilo-auto/frontier")
	}
}

func TestFetchKiloModelsFallsBackToStaticOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(server.Close)

	fallback := registry.GetKiloModels()
	if len(fallback) == 0 {
		t.Fatalf("static fallback is empty")
	}
	models := fetchKiloModels(context.Background(), nil, &config.Config{}, server.URL, "kilo", fallback)
	if len(models) != len(fallback) {
		t.Fatalf("models = %d, want %d (fallback)", len(models), len(fallback))
	}
}

func TestKiloPrepareRequestAnonymousFallback(t *testing.T) {
	e := NewKiloExecutor(nil)
	req, err := http.NewRequest(http.MethodPost, "https://api.kilo.ai/api/openrouter/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := e.PrepareRequest(req, nil); err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer anonymous" {
		t.Errorf("Authorization = %q, want Bearer anonymous", got)
	}
	if got := req.Header.Get("X-KiloCode-EditorName"); got != "CLIProxyAPIPlus" {
		t.Errorf("X-KiloCode-EditorName = %q, want CLIProxyAPIPlus", got)
	}
}

func TestKiloPrepareRequestRealTokenWins(t *testing.T) {
	e := NewKiloExecutor(nil)
	req, err := http.NewRequest(http.MethodPost, "https://api.kilo.ai/api/openrouter/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{"kilocodeToken": "real-token"},
	}
	if err := e.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer real-token" {
		t.Errorf("Authorization = %q, want Bearer real-token", got)
	}
}

func TestKiloPrepareRequestGatewayOmitsAuth(t *testing.T) {
	e := NewKiloExecutorForProvider(nil, "kilo-gateway")
	req, err := http.NewRequest(http.MethodPost, "https://api.kilo.ai/api/gateway/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := e.PrepareRequest(req, nil); err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty (gateway is optional auth)", got)
	}
	if got := req.Header.Get("X-KiloCode-EditorName"); got != "CLIProxyAPIPlus" {
		t.Errorf("X-KiloCode-EditorName = %q, want CLIProxyAPIPlus", got)
	}
}

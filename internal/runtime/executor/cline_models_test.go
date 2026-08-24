package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestFetchClineModelsUsesLiveCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("X-CLIENT-TYPE"); got != "cline-cli" {
			t.Errorf("X-CLIENT-TYPE = %q, want cline-cli", got)
		}
		// The client-identification headers must mirror the official CLI so the
		// API's version gate does not reject the catalog probe.
		if got := r.Header.Get("X-CLIENT-VERSION"); got != clineClientVersionPinned {
			t.Errorf("X-CLIENT-VERSION = %q, want %q", got, clineClientVersionPinned)
		}
		if got := r.Header.Get("X-CORE-VERSION"); got != clineCoreVersion {
			t.Errorf("X-CORE-VERSION = %q, want %q", got, clineCoreVersion)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [{
				"id": "provider/new-live-model",
				"name": "New live model",
				"description": "Returned by Cline",
				"context_length": 1048576,
				"max_tokens": 32768
			}]
		}`))
	}))
	t.Cleanup(server.Close)

	models := fetchClineModels(context.Background(), nil, &config.Config{}, server.URL)
	if len(models) != 1 {
		t.Fatalf("fetched models = %d, want 1", len(models))
	}

	model := models[0]
	if model.ID != "provider/new-live-model" {
		t.Errorf("model ID = %q, want provider/new-live-model", model.ID)
	}
	if model.DisplayName != "New live model" {
		t.Errorf("display name = %q, want New live model", model.DisplayName)
	}
	if model.ContextLength != 1048576 {
		t.Errorf("context length = %d, want 1048576", model.ContextLength)
	}
	if model.MaxCompletionTokens != 32768 {
		t.Errorf("max completion tokens = %d, want 32768", model.MaxCompletionTokens)
	}
	if model.OwnedBy != "cline" || model.Type != "cline" {
		t.Errorf("model ownership = %q/%q, want cline/cline", model.OwnedBy, model.Type)
	}
}

func TestFetchClineModelsFiltersFreeSuffixWhenConfigured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id":"openrouter/paid-model","name":"Paid model"},
				{"id":"openrouter/free-model:free","name":"Free model"},
				{"id":"openrouter/another:free-preview","name":"Free preview"}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	allModels := fetchClineModels(context.Background(), nil, &config.Config{}, server.URL)
	if len(allModels) != 3 {
		t.Fatalf("unfiltered models = %d, want 3", len(allModels))
	}

	freeModels := fetchClineModels(context.Background(), nil, &config.Config{
		ClineFreeModelsOnly: true,
	}, server.URL)
	if len(freeModels) != 2 {
		t.Fatalf("free-only models = %d, want 2", len(freeModels))
	}
	for _, model := range freeModels {
		if !strings.Contains(model.ID, ":free") {
			t.Fatalf("free-only result contains %q without :free", model.ID)
		}
	}
}

func TestFilterClineModelsAppliesToFallbackCatalog(t *testing.T) {
	models := []*registry.ModelInfo{
		{ID: "provider/paid"},
		{ID: "provider/free:free"},
	}

	filtered := FilterClineModels(models, true)
	if len(filtered) != 1 || filtered[0].ID != "provider/free:free" {
		t.Fatalf("filtered fallback models = %#v", filtered)
	}
}

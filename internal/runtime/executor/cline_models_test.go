package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestFetchClineModelsUsesLiveCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("X-CLIENT-TYPE"); got != "9router" {
			t.Errorf("X-CLIENT-TYPE = %q, want 9router", got)
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

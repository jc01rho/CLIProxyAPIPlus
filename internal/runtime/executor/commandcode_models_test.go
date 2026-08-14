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

func TestCommandCodeBuildModelsURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty uses default host", in: "", want: "https://api.commandcode.ai/provider/v1/models"},
		{name: "bare host", in: "https://api.commandcode.ai", want: "https://api.commandcode.ai/provider/v1/models"},
		{name: "trailing slash", in: "https://api.commandcode.ai/", want: "https://api.commandcode.ai/provider/v1/models"},
		{name: "legacy /v1 suffix", in: "https://api.commandcode.ai/v1", want: "https://api.commandcode.ai/provider/v1/models"},
		{name: "legacy /v1/models", in: "https://api.commandcode.ai/v1/models", want: "https://api.commandcode.ai/provider/v1/models"},
		{name: "already provider path", in: "https://api.commandcode.ai/provider/v1/models", want: "https://api.commandcode.ai/provider/v1/models"},
		{name: "custom host", in: "https://mock.commandcode.test", want: "https://mock.commandcode.test/provider/v1/models"},
		{name: "custom host /v1", in: "https://mock.commandcode.test/v1", want: "https://mock.commandcode.test/provider/v1/models"},
		{name: "provider prefix only", in: "https://api.commandcode.ai/provider", want: "https://api.commandcode.ai/provider/v1/models"},
		{name: "provider /v1 prefix", in: "https://api.commandcode.ai/provider/v1", want: "https://api.commandcode.ai/provider/v1/models"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandCodeBuildModelsURL(tt.in); got != tt.want {
				t.Fatalf("commandCodeBuildModelsURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFetchCommandCodeModelsUsesLiveCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/provider/v1/models" {
			t.Errorf("path = %s, want /provider/v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer user_live" {
			t.Errorf("Authorization = %q, want Bearer user_live", got)
		}
		if got := r.Header.Get("x-command-code-version"); got != commandCodeVersion {
			t.Errorf("x-command-code-version = %q, want %q", got, commandCodeVersion)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [{
				"id": "claude-sonnet-5",
				"object": "model",
				"created": 1786690443,
				"owned_by": "command-code",
				"name": "Claude Sonnet 5",
				"context_length": 1000000
			}, {
				"id": "live/new-model",
				"object": "model",
				"created": 1786690443,
				"owned_by": "command-code",
				"name": "Live New Model",
				"context_length": 256000
			}]
		}`))
	}))
	t.Cleanup(server.Close)

	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "user_live",
		"base_url": server.URL,
	}}
	models := FetchCommandCodeModels(context.Background(), auth, &config.Config{})

	var foundLive, foundStatic bool
	for _, model := range models {
		if model == nil {
			continue
		}
		switch model.ID {
		case "live/new-model":
			foundLive = true
			if model.DisplayName != "Live New Model" {
				t.Errorf("live display name = %q, want Live New Model", model.DisplayName)
			}
			if model.ContextLength != 256000 {
				t.Errorf("live context length = %d, want 256000", model.ContextLength)
			}
			if model.OwnedBy != "commandcode" || model.Type != "commandcode" {
				t.Errorf("ownership = %q/%q, want commandcode/commandcode", model.OwnedBy, model.Type)
			}
		case "claude-sonnet-5":
			foundStatic = true
			if model.DisplayName != "Claude Sonnet 5" {
				t.Errorf("static display name = %q, want Claude Sonnet 5", model.DisplayName)
			}
		}
	}
	if !foundLive {
		t.Fatalf("live model missing from merged catalog")
	}
	if !foundStatic {
		t.Fatalf("static catalog model missing after live overlay")
	}
	if len(models) < len(registry.GetCommandCodeModels()) {
		t.Fatalf("merged catalog = %d, want at least static %d", len(models), len(registry.GetCommandCodeModels()))
	}
}

func TestFetchCommandCodeModelsFallsBackToStaticOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(server.Close)

	fallback := registry.GetCommandCodeModels()
	if len(fallback) == 0 {
		t.Fatalf("static fallback is empty")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "user_fail",
		"base_url": server.URL,
	}}
	models := FetchCommandCodeModels(context.Background(), auth, &config.Config{})
	if len(models) != len(fallback) {
		t.Fatalf("models = %d, want %d (fallback)", len(models), len(fallback))
	}
}

func TestFetchCommandCodeModelsFallsBackOnMalformedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":"not-an-array"}`))
	}))
	t.Cleanup(server.Close)

	fallback := registry.GetCommandCodeModels()
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "user_bad",
		"base_url": server.URL,
	}}
	models := FetchCommandCodeModels(context.Background(), auth, &config.Config{})
	if len(models) != len(fallback) {
		t.Fatalf("models = %d, want %d (fallback)", len(models), len(fallback))
	}
}

func TestCommandCodeGenerateURLStillUsesAlphaGenerate(t *testing.T) {
	// The models endpoint change must not rewrite the generate URL. The
	// executor still posts to /alpha/generate even when the catalog lives
	// under /provider/v1/models.
	defaultAuth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "user_default"}}
	if got := commandCodeGenerateURL(defaultAuth); got != "https://api.commandcode.ai/alpha/generate" {
		t.Fatalf("default generate URL = %q", got)
	}
	if !strings.HasSuffix(commandCodeBuildModelsURL(""), "/provider/v1/models") {
		t.Fatalf("default models URL = %q, want /provider/v1/models suffix", commandCodeBuildModelsURL(""))
	}
}

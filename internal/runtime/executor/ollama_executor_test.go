package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestOllamaExecutorExecuteUsesChatAPI(t *testing.T) {
	var sawAuth string
	var sawPath string
	var payload map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-oss:120b","message":{"role":"assistant","content":"blue light scatters more"},"done":true,"prompt_eval_count":3,"eval_count":4}`))
	}))
	defer server.Close()

	exec := NewOllamaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "ollama-key", "base_url": server.URL}}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-oss:120b",
		Payload: []byte(`{"model":"gpt-oss:120b","messages":[{"role":"user","content":"Why is the sky blue?"}],"stream":false}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if sawPath != "/chat" {
		t.Fatalf("expected /chat path, got %q", sawPath)
	}
	if sawAuth != "Bearer ollama-key" {
		t.Fatalf("unexpected auth header %q", sawAuth)
	}
	if payload["model"] != "gpt-oss:120b" || payload["stream"] != false {
		t.Fatalf("unexpected ollama payload: %#v", payload)
	}
	if got := string(resp.Payload); !json.Valid(resp.Payload) || !containsAll(got, "blue light scatters more", "prompt_tokens", "completion_tokens") {
		t.Fatalf("unexpected response payload: %s", got)
	}
}

func TestParseOllamaTags(t *testing.T) {
	models := parseOllamaTags([]byte(`{"models":[{"name":"gpt-oss:120b"},{"model":"llama3.3"},{"name":"gpt-oss:120b"}]}`))
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "gpt-oss:120b" || models[1].ID != "llama3.3" {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestOllamaExecutor_UsesExecutionTargetForAliasedModel(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	const authID = "auth-ollama-alias"
	reg.UnregisterClient(authID)
	defer reg.UnregisterClient(authID)

	reg.RegisterClient(authID, "ollama-api-key", []*registry.ModelInfo{
		{ID: "higher-coding", ExecutionTarget: "kimi-k2.6", DisplayName: "Higher Coding alias"},
		{ID: "kimi-k2.6", DisplayName: "Kimi K2.6"},
	})

	var requestModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requestModels = append(requestModels, payload["model"].(string))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"kimi-k2.6","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":1,"eval_count":1}`))
	}))
	defer server.Close()

	exec := NewOllamaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       authID,
		Provider: "ollama-api-key",
		Attributes: map[string]string{
			"api_key":  "ollama-key",
			"base_url": server.URL,
		},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "higher-coding",
		Payload: []byte(`{"model":"higher-coding","messages":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(requestModels) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requestModels))
	}
	if requestModels[0] != "kimi-k2.6" {
		t.Fatalf("upstream model = %q, want %q", requestModels[0], "kimi-k2.6")
	}
	if !json.Valid(resp.Payload) {
		t.Fatalf("response payload is not valid JSON: %s", string(resp.Payload))
	}
}

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}

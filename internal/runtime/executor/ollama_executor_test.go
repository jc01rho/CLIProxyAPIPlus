package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}

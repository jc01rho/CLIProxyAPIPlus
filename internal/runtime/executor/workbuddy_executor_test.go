package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/workbuddy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func workBuddyTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "workbuddy-test", Provider: "workbuddy", Metadata: map[string]any{
		"access_token": "token", "user_id": "uid", "domain": workbuddy.DefaultDomain,
	}}
}

func TestWorkBuddyExecutorUsesConsolePathThenV2Fallback(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == workBuddyChatPaths[0] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":404}`)
			return
		}
		if got := r.Header.Get("Origin"); got != workbuddy.BaseURL {
			t.Errorf("Origin = %q, want %q", got, workbuddy.BaseURL)
		}
		if got := r.Header.Get("X-No-Enterprise-Id"); got != "1" {
			t.Errorf("X-No-Enterprise-Id = %q, want 1", got)
		}
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Messages) < 2 || payload.Messages[0].Role != "system" {
			t.Errorf("messages = %#v, want prepended system message", payload.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"id":"chatcmpl-workbuddy","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			`data: [DONE]`, "",
		}, "\n"))
	}))
	t.Cleanup(server.Close)
	previousBase := workBuddyBaseURL
	workBuddyBaseURL = server.URL
	t.Cleanup(func() { workBuddyBaseURL = previousBase })

	exec := NewWorkBuddyExecutor(&config.Config{})
	resp, err := exec.Execute(context.Background(), workBuddyTestAuth(), cliproxyexecutor.Request{
		Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(paths) != 2 || paths[0] != workBuddyChatPaths[0] || paths[1] != workBuddyChatPaths[1] {
		t.Fatalf("paths = %v, want fallback sequence %v", paths, workBuddyChatPaths)
	}
	if !strings.Contains(string(resp.Payload), "ok") {
		t.Fatalf("response = %s, want content", resp.Payload)
	}
}

func TestParseWorkBuddyModelsNormalizesOwnedBy(t *testing.T) {
	models := parseWorkBuddyModels([]byte(`{"code":0,"data":{"models":[{"id":"model-a","vendor":"f"},{"id":"model-b","vendor":"workbuddy"}]}}`))
	if len(models) != 2 {
		t.Fatalf("models = %#v, want 2 entries", models)
	}
	for _, model := range models {
		if model.OwnedBy != "workbuddy" {
			t.Errorf("model %q owned_by = %q, want workbuddy", model.ID, model.OwnedBy)
		}
	}
}

func TestWorkBuddyAliasBaseModelIDs(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"workbuddy": {
			{Name: "deepseek-v4.1-flash", Alias: "wb1", Fork: true},
			{Name: "ignored", Alias: "no-fork"},
			{Name: "deepseek-v4.1-flash", Alias: "wb1-copy", Fork: true},
		},
	}}
	got := workBuddyAliasBaseModelIDs(cfg)
	if len(got) != 1 || got[0] != "deepseek-v4.1-flash" {
		t.Fatalf("required alias base IDs = %#v, want [deepseek-v4.1-flash]", got)
	}
}

func TestFetchWorkBuddyModelsUsesLiveCatalogAndFallsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != workBuddyModelPaths[0] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"models":[{"id":"live-workbuddy","name":"Live WorkBuddy","maxInputTokens":131072,"maxOutputTokens":8192,"reasoning":{"supportedEfforts":["low","high"]}}]}}`)
	}))
	previousBase := workBuddyBaseURL
	workBuddyBaseURL = server.URL
	t.Cleanup(func() {
		workBuddyBaseURL = previousBase
		server.Close()
	})

	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"workbuddy": {{Name: "deepseek-v4.1-flash", Alias: "wb1", Fork: true}},
	}}
	models := FetchWorkBuddyModels(context.Background(), workBuddyTestAuth(), cfg)
	if len(models) != 2 || models[0].ID != "live-workbuddy" || models[1].ID != "deepseek-v4.1-flash" {
		t.Fatalf("live models = %#v", models)
	}
	if models[0].Type != "workbuddy" || models[1].Type != "workbuddy" {
		t.Fatalf("live model types = %#v", models)
	}
	if models[0].Thinking == nil || len(models[0].Thinking.Levels) != 2 {
		t.Fatalf("live thinking metadata = %#v", models[0].Thinking)
	}

	server.Close()
	fallback := FetchWorkBuddyModels(context.Background(), workBuddyTestAuth(), &config.Config{})
	if len(fallback) == 0 || fallback[0].Type != "workbuddy" {
		t.Fatalf("fallback models = %#v", fallback)
	}
}

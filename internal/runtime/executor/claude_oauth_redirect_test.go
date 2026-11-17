package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A custom base_url still receives the OAuth request, but a redirect it answers
// with is not followed, so the bearer token never reaches the redirect target.
func TestClaudeExecutor_OAuthRequestDoesNotFollowRedirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	var originHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		http.Redirect(w, r, target.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	auth := &cliproxyauth.Auth{ID: "redirect-oauth", Provider: "claude", Metadata: claudeOAuthTestMetadata(), Attributes: map[string]string{
		"api_key": "sk-ant-oat-redirect", "base_url": origin.URL,
	}}
	payload := []byte(`{"model":"claude-opus-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	_, err := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil || !errors.Is(err, claudeauth.ErrOAuthRedirectRefused) {
		t.Fatalf("Execute() error = %v, want ErrOAuthRedirectRefused", err)
	}
	if originHits.Load() != 1 {
		t.Fatalf("custom base_url hits = %d, want 1", originHits.Load())
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target hits = %d, want 0", targetHits.Load())
	}
}

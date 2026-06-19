package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	antigravity "github.com/router-for-me/CLIProxyAPI/v7/internal/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestAntigravityBuildRequest_SignsBillingHeaderCCH(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	payload := []byte(`{
		"request": {
			"contents": [{"role":"user","parts":[{"text":"hi"}]}],
			"systemInstruction": {"parts": [{"text":"x-anthropic-billing-header: cc_version=2.1.177.3bf; cc_entrypoint=cli; cch=00000;"}]}
		}
	}`)

	req, err := executor.buildRequest(context.Background(), auth, "token", "claude-sonnet-4-6", payload, false, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	raw := requestRawBody(t, req)

	if strings.Contains(raw, "cch=00000;") {
		t.Fatalf("billing header CCH placeholder was not signed: %s", raw)
	}
	if got := antigravity.ExtractBillingHeaderCCH(raw); got == "" {
		t.Fatalf("signed billing header CCH missing: %s", raw)
	}
}

func TestAntigravityBuildRequest_AppliesTransformsAndCrossModelSanitizer(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	payload := []byte(`{
		"request": {
			"contents": [{"role":"user","parts":[{"text":"hi","thoughtSignature":"gemini-signature"}]}],
			"generationConfig": {"stop_sequences": ["stop"]}
		}
	}`)

	req, err := executor.buildRequest(context.Background(), auth, "token", "claude-sonnet-4-6", payload, false, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	body := requestBody(t, req)
	request := body["request"].(map[string]any)
	generationConfig := request["generationConfig"].(map[string]any)
	if _, ok := generationConfig["stop_sequences"]; ok {
		t.Fatalf("Claude transform should convert stop_sequences, got %v", generationConfig)
	}
	if got, ok := generationConfig["stopSequences"].([]any); !ok || len(got) != 1 || got[0] != "stop" {
		t.Fatalf("Claude transform stopSequences = %v, want [stop]", generationConfig["stopSequences"])
	}
	contents := request["contents"].([]any)
	part := contents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if _, ok := part["thoughtSignature"]; ok {
		t.Fatalf("cross-model sanitizer should strip Gemini thoughtSignature for Claude target: %v", part)
	}
}

func TestAntigravityBuildRequest_AppliesGeminiToolTransforms(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	payload := []byte(`{
		"request": {
			"contents": [{"role":"user","parts":[{"text":"hi"}]}],
			"tools": [{"name":"read_file","parameters":{"type":"object"}}]
		}
	}`)

	req, err := executor.buildRequest(context.Background(), auth, "token", "gemini-3.1-pro", payload, false, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	body := requestBody(t, req)
	decl := extractFirstFunctionDeclaration(t, body)
	if got := decl["name"]; got != "read_file" {
		t.Fatalf("Gemini transform declaration name = %v, want read_file", got)
	}
}

func TestAntigravityPrepareRequestAuth_EnsureProjectContextUsesPersistedManagedProject(t *testing.T) {
	antigravity.InvalidateProjectContextCache("")
	t.Cleanup(func() { antigravity.InvalidateProjectContextCache("") })
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token":  "token",
		"refresh_token": "refresh-token|base-project|managed-project",
		"expired":       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}

	updated, err := executor.PrepareRequestAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("PrepareRequestAuth error: %v", err)
	}
	if got := updated.Metadata["project_id"]; got != "managed-project" {
		t.Fatalf("project_id = %v, want managed-project", got)
	}
	if got := updated.Metadata["refresh_token"]; got != "refresh-token|base-project|managed-project" {
		t.Fatalf("refresh_token = %v, want managed project preserved", got)
	}
}

func TestAntigravityExecute_RecordsHealthAndConsumesTokenBucket(t *testing.T) {
	health := antigravity.InitHealthTracker(antigravity.HealthScoreConfig{Initial: 10, SuccessReward: 5, RateLimitPenalty: -4, FailurePenalty: -2, MaxScore: 100, MinUsable: 1})
	antigravity.InitTokenTracker(antigravity.TokenBucketConfig{MaxTokens: 1.5, InitialTokens: 1.5, RegenerationRatePerMinute: 0.1})
	t.Cleanup(func() {
		antigravity.InitHealthTracker(antigravity.HealthScoreConfig{})
		antigravity.InitTokenTracker(antigravity.TokenBucketConfig{})
	})

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:         "health-success-auth",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	exec := NewAntigravityExecutor(&config.Config{})
	request := cliproxyexecutor.Request{
		Model:   "gemini-2.5-pro",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}
	if _, err := exec.Execute(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if _, err := exec.Execute(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}); err == nil {
		t.Fatal("second Execute() error = nil, want local token bucket rate limit after consumption")
	}

	if requests != 1 {
		t.Fatalf("HTTP requests = %d, want second call blocked by consumed token", requests)
	}
	snapshot := health.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("health snapshot entries = %d, want 1", len(snapshot))
	}
	for _, entry := range snapshot {
		if entry.Score <= 10 {
			t.Fatalf("health score = %v, want success reward above initial", entry.Score)
		}
	}
}

func TestAntigravityExecute_TokenBucketExhaustionSkipsHTTP(t *testing.T) {
	antigravity.InitTokenTracker(antigravity.TokenBucketConfig{MaxTokens: 1, InitialTokens: 0.5, RegenerationRatePerMinute: 0.1})
	t.Cleanup(func() { antigravity.InitTokenTracker(antigravity.TokenBucketConfig{}) })

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:         "bucket-empty-auth",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	exec := NewAntigravityExecutor(&config.Config{})
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-pro",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity})
	if err == nil {
		t.Fatal("Execute() error = nil, want local rate limit")
	}
	if requests != 0 {
		t.Fatalf("HTTP requests = %d, want token bucket to block before send", requests)
	}
}

func TestAntigravityExecute_RecoverableErrorInvalidatesProjectContext(t *testing.T) {
	antigravity.InvalidateProjectContextCache("")
	t.Cleanup(func() { antigravity.InvalidateProjectContextCache("") })

	exec := NewAntigravityExecutor(&config.Config{})
	prepared := &cliproxyauth.Auth{
		ID:         "recoverable-auth",
		Attributes: map[string]string{},
		Metadata: map[string]any{
			"access_token":  "token",
			"refresh_token": "refresh-token|base-project|managed-one",
			"project_id":    "managed-one",
			"expired":       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "Expected thinking block first, found text"}})
	}))
	defer server.Close()
	prepared.Attributes = map[string]string{"base_url": server.URL}
	_, _ = exec.Execute(context.Background(), prepared, cliproxyexecutor.Request{
		Model:   "gemini-2.5-pro",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity})

	if got := prepared.Metadata["project_id"]; got != nil {
		t.Fatalf("recoverable error should clear project_id, got %v", got)
	}
	if got := prepared.Metadata["refresh_token"]; got != "refresh-token|base-project" {
		t.Fatalf("recoverable error should drop managed project from refresh token, got %v", got)
	}
}

func requestRawBody(t *testing.T, req *http.Request) string {
	t.Helper()
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body error: %v", err)
	}
	return string(raw)
}

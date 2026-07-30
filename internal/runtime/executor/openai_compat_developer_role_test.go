package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatDeveloperRoleRetryRequiresStructuredSpecificEvidence(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	key := executor.developerRoleCapabilityKey(nil, "https://user:secret@example.com/v1?api_key=secret", "model-a", "/chat/completions")
	payload := []byte(`{"messages":[{"role":"developer","content":"policy"},{"role":"user","content":"hi"}]}`)

	for _, response := range [][]byte{
		[]byte(`developer unsupported role`),
		[]byte(`{"error":{"message":"invalid request body"}}`),
		[]byte(`{"error":{"message":"developer quota exceeded"}}`),
	} {
		if _, retry := executor.developerRoleRetryPayload(key, payload, response, http.StatusBadRequest); retry {
			t.Fatalf("unexpected retry for %s", response)
		}
	}
	if _, retry := executor.developerRoleRetryPayload(key, payload, []byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`), http.StatusBadRequest); !retry {
		t.Fatal("expected retry for structured unsupported developer role")
	}
	if strings.Contains(key.baseURL, "secret") || strings.Contains(key.baseURL, "api_key") {
		t.Fatalf("capability base URL retained credentials or query: %q", key.baseURL)
	}
}

func TestOpenAICompatDeveloperRoleRetryRejectsLaterOrMixedDeveloper(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	key := executor.developerRoleCapabilityKey(nil, "https://example.com/v1", "model-a", "/chat/completions")
	evidence := []byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`)
	payloads := [][]byte{
		[]byte(`{"messages":[{"role":"user","content":"hi"},{"role":"developer","content":"late"}]}`),
		[]byte(`{"messages":[{"role":"developer","content":"first"},{"role":"user","content":"hi"},{"role":"developer","content":"late"}]}`),
	}
	for _, payload := range payloads {
		rewritten, retry := executor.developerRoleRetryPayload(key, payload, evidence, http.StatusBadRequest)
		if retry || string(rewritten) != string(payload) {
			t.Fatalf("later/mixed developer payload retried or changed: %s", payload)
		}
	}
}

func TestOpenAICompatDeveloperRoleCacheIsolationTTLAndBound(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newOpenAICompatDeveloperRoleCache(func() time.Time { return now })
	base := openAICompatDeveloperRoleCapabilityKey{identity: "provider", baseURL: "https://example.com/v1", model: "model-a", endpointFamily: "/chat/completions"}
	cache.add(base)
	if !cache.contains(base) {
		t.Fatal("cached capability missing")
	}
	modelB := base
	modelB.model = "model-b"
	if cache.contains(modelB) {
		t.Fatal("cache leaked across models")
	}
	compact := base
	compact.endpointFamily = "/responses/compact"
	if cache.contains(compact) {
		t.Fatal("cache leaked across endpoint families")
	}
	now = now.Add(openAICompatDeveloperRoleCacheTTL)
	if cache.contains(base) {
		t.Fatal("expired cache entry remained active")
	}

	for index := 0; index < openAICompatDeveloperRoleCacheMax+10; index++ {
		key := base
		key.model = string(rune(index + 1))
		cache.add(key)
		now = now.Add(time.Nanosecond)
	}
	cache.mu.Lock()
	gotLen := len(cache.entries)
	cache.mu.Unlock()
	if gotLen != openAICompatDeveloperRoleCacheMax {
		t.Fatalf("cache size = %d, want %d", gotLen, openAICompatDeveloperRoleCacheMax)
	}
}

func TestOpenAICompatExecutorDoesNotRetryUnrelatedOrRepeatedRejection(t *testing.T) {
	t.Run("unrelated", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid temperature"}}`))
		}))
		defer server.Close()
		executor, auth := newDeveloperRoleTestExecutor(server.URL)
		_, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
		if err == nil || requests != 1 {
			t.Fatalf("err=%v requests=%d, want error and one request", err, requests)
		}
	})

	t.Run("repeated rejection", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`))
		}))
		defer server.Close()
		executor, auth := newDeveloperRoleTestExecutor(server.URL)
		_, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
		if err == nil || requests != 2 {
			t.Fatalf("err=%v requests=%d, want error and two requests", err, requests)
		}
	})
}

func TestOpenAICompatExecutorStreamDoesNotRetryAfterContent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"visible\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"unsupported role: developer\",\"type\":\"invalid_request_error\"}}\n\n"))
	}))
	defer server.Close()
	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	result, err := executor.ExecuteStream(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var output strings.Builder
	var terminal error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			terminal = chunk.Err
			continue
		}
		output.Write(chunk.Payload)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if !strings.Contains(output.String(), "visible") {
		t.Fatalf("content not emitted: %s", output.String())
	}
	if terminal == nil {
		t.Fatal("expected terminal stream error after content")
	}
}

func TestOpenAICompatKnownFallbackIsolatedByModelAndEndpointFamily(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility"}
	chatA := executor.developerRoleCapabilityKey(auth, "https://example.com/v1", "model-a", "/chat/completions")
	executor.developerRoleCacheInstance().add(chatA)
	payload := []byte(`{"messages":[{"role":"developer","content":"policy"},{"role":"user","content":"hi"}]}`)
	if role := gjson.GetBytes(executor.applyKnownDeveloperRoleFallback(chatA, payload), "messages.0.role").String(); role != "system" {
		t.Fatalf("known fallback role = %q, want system", role)
	}
	chatB := executor.developerRoleCapabilityKey(auth, "https://example.com/v1", "model-b", "/chat/completions")
	if role := gjson.GetBytes(executor.applyKnownDeveloperRoleFallback(chatB, payload), "messages.0.role").String(); role != "developer" {
		t.Fatalf("model-isolated role = %q, want developer", role)
	}
	compact := executor.developerRoleCapabilityKey(auth, "https://example.com/v1", "model-a", "/responses/compact")
	if role := gjson.GetBytes(executor.applyKnownDeveloperRoleFallback(compact, payload), "messages.0.role").String(); role != "developer" {
		t.Fatalf("endpoint-isolated role = %q, want developer", role)
	}
}

func newDeveloperRoleTestExecutor(serverURL string) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
	return NewOpenAICompatExecutor("openai-compatibility", &config.Config{}), &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": serverURL + "/v1",
		"api_key":  "test",
	}}
}

func developerRoleTestRequest() cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   "model-a",
		Payload: []byte(`{"model":"model-a","messages":[{"role":"developer","content":"policy"},{"role":"user","content":"hi"}]}`),
	}
}

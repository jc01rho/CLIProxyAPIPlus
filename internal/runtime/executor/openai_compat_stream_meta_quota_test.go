package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const metaMuseStreamFixture = "" +
	`data: {"id":"1","choices":[{"delta":{"role":"assistant"}}]}` + "\n\n" +
	"event: response.subscription_usage\n" +
	`data: {"type":"response.subscription_usage","subscription":{"window":{"used_percent":42.5,"resets_at":1788431188,"window_duration_mins":300},"weekly":{"used_percent":63,"resets_at":1788739200}}}` + "\n\n" +
	`data: {"id":"1","choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
	`data: [DONE]` + "\n\n"

func drainOpenAICompatStream(t *testing.T, result *cliproxyexecutor.StreamResult) {
	t.Helper()
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}
}

func TestOpenAICompatExecutorStream_when_MetaSubscriptionUsageEventArrives(t *testing.T) {
	// Given
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, metaMuseStreamFixture)
	}))
	defer server.Close()
	exec := NewOpenAICompatExecutor("openai-compatible-meta", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test"}}

	// When
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	drainOpenAICompatStream(t, result)

	// Then
	if requestCount != 1 {
		t.Fatalf("upstream request count = %d, want 1", requestCount)
	}
	if got := result.Headers.Get("X-Muse-Fivehour-Reset-At"); got != "1788431188" {
		t.Fatalf("five-hour reset = %q, want 1788431188", got)
	}
	if got := result.Headers.Get("X-Muse-Weekly-Reset-At"); got != "1788739200" {
		t.Fatalf("weekly reset = %q, want 1788739200", got)
	}
}

func TestOpenAICompatExecutorStream_when_NonMetaProviderEmitsSameEvent(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, metaMuseStreamFixture)
	}))
	defer server.Close()
	exec := NewOpenAICompatExecutor("openai-compatible-other", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test"}}

	// When
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "model-a",
		Payload: []byte(`{"model":"model-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	drainOpenAICompatStream(t, result)

	// Then
	if got := result.Headers.Get("X-Muse-Fivehour-Used-Percent"); got != "" {
		t.Fatalf("Meta signal leaked from non-Meta provider: %q", got)
	}
}

package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudeAdaptiveOnlyErrorBody = `{"type":"error","error":{"type":"invalid_request_error","message":"\"thinking.type.disabled\" is not supported for this model. Use \"thinking.type.adaptive\" and \"output_config.effort\" to control thinking behavior."},"request_id":"req_test"}`

// adaptiveOnlyUpstream mimics a model that rejects thinking.type "disabled":
// it answers 400 for disabled thinking and succeeds for anything else.
type adaptiveOnlyUpstream struct {
	mu        sync.Mutex
	bodies    [][]byte
	always400 bool
	stream    bool
}

func (u *adaptiveOnlyUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()
	if u.always400 || gjson.GetBytes(body, "thinking.type").String() == "disabled" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(claudeAdaptiveOnlyErrorBody))
		return
	}
	if u.stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
}

func (u *adaptiveOnlyUpstream) sent() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]byte(nil), u.bodies...)
}

func newAdaptiveRetryTestExecutor(t *testing.T, upstream *adaptiveOnlyUpstream) (*ClaudeExecutor, *cliproxyauth.Auth) {
	t.Helper()
	claudeAdaptiveThinkingOnlyModels.Delete("claude-opus-5-5")
	t.Cleanup(func() { claudeAdaptiveThinkingOnlyModels.Delete("claude-opus-5-5") })
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-adaptive-retry-test",
		"base_url": server.URL,
	}}
	return NewClaudeExecutor(&config.Config{}), auth
}

func adaptiveRetryTestRequest(payload string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	return cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(payload)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
}

const disabledThinkingPayload = `{"model":"claude-opus-5-5","max_tokens":1024,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`

func assertAdaptiveLowEffort(t *testing.T, body []byte) {
	t.Helper()
	if got := gjson.GetBytes(body, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "output_config.effort").String(); got != "low" {
		t.Fatalf("output_config.effort = %q, want low; body=%s", got, body)
	}
}

func TestClaudeExecutorRetriesAdaptiveThinkingWhenDisabledRejected(t *testing.T) {
	upstream := &adaptiveOnlyUpstream{}
	executor, auth := newAdaptiveRetryTestExecutor(t, upstream)
	req, opts := adaptiveRetryTestRequest(disabledThinkingPayload)

	if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v, want automatic adaptive retry to succeed", err)
	}
	sent := upstream.sent()
	if len(sent) != 2 {
		t.Fatalf("upstream requests = %d, want 2 (rejected + retry)", len(sent))
	}
	assertAdaptiveLowEffort(t, sent[1])

	// The model is remembered: the next request goes out adaptive on the first try.
	if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	sent = upstream.sent()
	if len(sent) != 3 {
		t.Fatalf("upstream requests = %d, want 3 (remembered model must not fail again)", len(sent))
	}
	assertAdaptiveLowEffort(t, sent[2])
}

func TestClaudeExecutorStreamRetriesAdaptiveThinkingWhenDisabledRejected(t *testing.T) {
	upstream := &adaptiveOnlyUpstream{stream: true}
	executor, auth := newAdaptiveRetryTestExecutor(t, upstream)
	req, opts := adaptiveRetryTestRequest(disabledThinkingPayload)
	opts.Stream = true

	result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v, want automatic adaptive retry to succeed", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}
	sent := upstream.sent()
	if len(sent) != 2 {
		t.Fatalf("upstream requests = %d, want 2 (rejected + retry)", len(sent))
	}
	assertAdaptiveLowEffort(t, sent[1])
}

func TestClaudeExecutorAdaptiveThinkingRetryRunsOnlyOnce(t *testing.T) {
	upstream := &adaptiveOnlyUpstream{always400: true}
	executor, auth := newAdaptiveRetryTestExecutor(t, upstream)
	req, opts := adaptiveRetryTestRequest(disabledThinkingPayload)

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("Execute() error = nil, want the upstream 400 after the single retry")
	}
	if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("Execute() error = %v, want status 400", err)
	}
	if got := len(upstream.sent()); got != 2 {
		t.Fatalf("upstream requests = %d, want exactly 2", got)
	}
}

func TestClaudeExecutorDoesNotRetryUnrelatedBadRequest(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: must be greater than 0"}}`))
	}))
	defer server.Close()
	claudeAdaptiveThinkingOnlyModels.Delete("claude-opus-5-5")
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-adaptive-retry-test", "base_url": server.URL}}
	req, opts := adaptiveRetryTestRequest(disabledThinkingPayload)

	if _, err := executor.Execute(context.Background(), auth, req, opts); err == nil {
		t.Fatal("Execute() error = nil, want upstream 400")
	}
	if requests != 1 {
		t.Fatalf("upstream requests = %d, want 1 (unrelated 400 must not retry)", requests)
	}
	if _, remembered := claudeAdaptiveThinkingOnlyModels.Load("claude-opus-5-5"); remembered {
		t.Fatal("unrelated 400 must not mark the model adaptive-only")
	}
}

func TestForceAdaptiveThinkingForAdaptiveOnlyModel(t *testing.T) {
	claudeAdaptiveThinkingOnlyModels.Store("claude-opus-5-5", struct{}{})
	t.Cleanup(func() { claudeAdaptiveThinkingOnlyModels.Delete("claude-opus-5-5") })

	manual := forceAdaptiveThinkingForAdaptiveOnlyModel([]byte(`{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":4096}}`))
	if got := gjson.GetBytes(manual, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("manual thinking.type = %q, want adaptive", got)
	}
	if gjson.GetBytes(manual, "thinking.budget_tokens").Exists() || gjson.GetBytes(manual, "output_config.effort").Exists() {
		t.Fatalf("manual budget must be dropped without inventing an effort: %s", manual)
	}

	keepEffort := forceAdaptiveThinkingForAdaptiveOnlyModel([]byte(`{"model":"claude-opus-5-5","thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`))
	if got := gjson.GetBytes(keepEffort, "output_config.effort").String(); got != "high" {
		t.Fatalf("caller effort = %q, want high preserved", got)
	}

	other := []byte(`{"model":"claude-sonnet-5","thinking":{"type":"disabled"}}`)
	if got := forceAdaptiveThinkingForAdaptiveOnlyModel(other); string(got) != string(other) {
		t.Fatalf("models that never rejected disabled thinking must be untouched: %s", got)
	}
}

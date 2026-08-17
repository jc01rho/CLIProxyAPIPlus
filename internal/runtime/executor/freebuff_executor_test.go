package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestBuildFreebuffPayloadAddsLifecycleMetadataAndBuffyMarker(t *testing.T) {
	payload, err := buildFreebuffPayload([]byte(`{
		"model":"alias",
		"messages":[{"role":"user","content":"hello"}],
		"runId":"caller-top",
		"cost_mode":"paid",
		"codebuff_metadata":{"run_id":"caller","cost_mode":"paid"}
	}`), "deepseek/deepseek-v4-flash", "base2-free-deepseek-flash", "run-1", "instance-1")
	if err != nil {
		t.Fatalf("buildFreebuffPayload() error = %v", err)
	}
	body := string(payload)
	for _, expected := range []string{
		`"model":"deepseek/deepseek-v4-flash"`,
		`"stream":true`,
		`"include_usage":true`,
		`"run_id":"run-1"`,
		`"n":"base2-free-deepseek-flash"`,
		`"cost_mode":"free"`,
		`"freebuff_instance_id":"instance-1"`,
		freebuffMarker,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("payload missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `"run_id":"caller"`) || strings.Contains(body, `"cost_mode":"paid"`) {
		t.Fatalf("caller metadata was not replaced: %s", body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, exists := decoded["runId"]; exists {
		t.Fatalf("caller top-level runId survived: %s", body)
	}
	if _, exists := decoded["cost_mode"]; exists {
		t.Fatalf("caller top-level cost_mode survived: %s", body)
	}
}

func TestConsumeFreebuffSSENormalizesReasoningAndKeepsToolFragments(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"think"}}]}`,
		"",
		`data: {"id":"chat-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-","type":"function","function":{"name":"look","arguments":"{\"q\":\""}}]}}]}`,
		"",
		`data: {"id":"chat-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"up","arguments":"x\"}"}}]},"finish_reason":"tool-calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	var chunks []map[string]any
	var rawEvents [][]byte
	if err := consumeFreebuffSSE(strings.NewReader(stream), func(raw []byte, chunk map[string]any) error {
		rawEvents = append(rawEvents, raw)
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatalf("consumeFreebuffSSE() error = %v", err)
	}
	if len(chunks) != 4 || chunks[3] != nil || string(rawEvents[3]) != "data: [DONE]" {
		t.Fatalf("callbacks = %d, final chunk = %#v, final raw = %q", len(chunks), chunks[len(chunks)-1], rawEvents[len(rawEvents)-1])
	}
	delta := chunks[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["reasoning_content"] != "think" {
		t.Fatalf("reasoning_content = %#v", delta["reasoning_content"])
	}
	acc := newFreebuffAccumulator()
	for _, chunk := range chunks[:3] {
		acc.add(chunk)
	}
	body := string(acc.response("deepseek/deepseek-v4-flash"))
	for _, expected := range []string{`"reasoning_content":"think"`, `"id":"call-1"`, `"name":"lookup"`, `"arguments":"{\"q\":\"x\"}"`, `"finish_reason":"tool_calls"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("aggregated response missing %q: %s", expected, body)
		}
	}
}

func TestFreebuffSessionGoneReadmits(t *testing.T) {
	var gets, posts int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/freebuff/session", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
			w.WriteHeader(http.StatusGone)
		case http.MethodPost:
			posts++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "active", "model": "deepseek/deepseek-v4-flash", "instanceId": "instance-2",
			})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "freebuff-gone", Attributes: map[string]string{
		"api_key": "token", "base_url": server.URL,
	}}
	state := executor.stateFor(auth)
	session, err := executor.ensureSession(t.Context(), auth, state, "deepseek/deepseek-v4-flash")
	if err != nil {
		t.Fatalf("ensureSession() error = %v", err)
	}
	if session.instanceID != "instance-2" || gets != 1 || posts != 1 {
		t.Fatalf("session = %#v, gets = %d, posts = %d", session, gets, posts)
	}
}

func TestFreebuffSessionAdmissionRejectsInvalidPostState(t *testing.T) {
	tests := []struct {
		name   string
		status string
		model  string
	}{
		{name: "inactive", status: "none", model: "model-a"},
		{name: "ended", status: "ended", model: "model-a"},
		{name: "model locked", status: "model_locked", model: "model-a"},
		{name: "wrong model", status: "active", model: "model-b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, `{"status":"none"}`)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": test.status, "model": test.model, "instanceId": "invalid-instance",
				})
			}))
			defer server.Close()

			executor := NewFreebuffExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				cliproxyauth.AttributeAPIKey: "token-" + test.name, "base_url": server.URL,
			}}
			state := executor.stateFor(auth)
			if _, err := executor.ensureSession(t.Context(), auth, state, "model-a"); err == nil {
				t.Fatal("ensureSession error = nil, want invalid admission error")
			}
			if state.session != nil {
				t.Fatalf("invalid session cached: %#v", state.session)
			}
		})
	}
}

func TestFreebuffCachedSessionRefreshErrorIsNotRetriedUnscoped(t *testing.T) {
	var gets, posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		case http.MethodPost:
			posts++
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "freebuff-refresh-error", Attributes: map[string]string{
		cliproxyauth.AttributeAPIKey: "token", "base_url": server.URL,
	}}
	state := executor.stateFor(auth)
	state.session = &freebuffSession{model: "model-a", instanceID: "instance-a"}
	_, err := executor.ensureSession(t.Context(), auth, state, "model-a")
	if err == nil {
		t.Fatal("ensureSession error = nil, want refresh error")
	}
	if gets != 1 || posts != 0 {
		t.Fatalf("gets = %d, posts = %d, want 1 and 0", gets, posts)
	}
}

func TestFreebuffCachedSessionModelSwitchDeletesBeforeAdmission(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		switch r.Method {
		case http.MethodDelete:
			if got := r.Header.Get("x-freebuff-instance-id"); got != "instance-a" {
				t.Fatalf("DELETE instance = %q, want instance-a", got)
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "active", "model": "model-b", "instanceId": "instance-b",
			})
		default:
			http.Error(w, `{"error":"unexpected GET"}`, http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "freebuff-model-switch", Attributes: map[string]string{
		cliproxyauth.AttributeAPIKey: "token", "base_url": server.URL,
	}}
	state := executor.stateFor(auth)
	state.session = &freebuffSession{model: "model-a", instanceID: "instance-a"}
	session, err := executor.ensureSession(t.Context(), auth, state, "model-b")
	if err != nil {
		t.Fatalf("ensureSession error = %v", err)
	}
	if session.instanceID != "instance-b" {
		t.Fatalf("session = %#v, want instance-b", session)
	}
	if got := strings.Join(methods, ","); got != "DELETE,POST" {
		t.Fatalf("methods = %s, want DELETE,POST", got)
	}
}

func TestFreebuffExecutorsShareCredentialStateAcrossReplacement(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "freebuff-shared-state", Attributes: map[string]string{"api_key": "token"}}
	first := NewFreebuffExecutor(&config.Config{})
	second := NewFreebuffExecutor(&config.Config{})
	if first.stateFor(auth) != second.stateFor(auth) {
		t.Fatal("replacement executor created an independent credential lease")
	}
}

func TestFreebuffResolveModelUsesSelectedConfigEntry(t *testing.T) {
	executor := NewFreebuffExecutor(&config.Config{FreebuffKey: []config.FreebuffKey{
		{APIKey: "same-token", Models: []config.FreebuffModel{{Name: "model-a", Alias: "shared", AgentID: "agent-a"}}},
		{APIKey: "same-token", Models: []config.FreebuffModel{{Name: "model-b", Alias: "shared", AgentID: "agent-b"}}},
	}})
	auth := &cliproxyauth.Auth{ID: "selected-second", Attributes: map[string]string{
		"api_key": "same-token", "config_index": "1",
	}}
	model, agentID := executor.resolveModel(auth, "shared")
	if model != "model-b" || agentID != "agent-b" {
		t.Fatalf("resolveModel() = %q, %q", model, agentID)
	}
}

func TestReadFreebuffBodyTruncates(t *testing.T) {
	got, err := readFreebuffBody(strings.NewReader("0123456789"), 4)
	if err != nil {
		t.Fatalf("readFreebuffBody() error = %v", err)
	}
	if string(got) != "0123...[truncated]" {
		t.Fatalf("readFreebuffBody() = %q", got)
	}
}

func TestFreebuffErrorMessageDoesNotLeakUpstreamBody(t *testing.T) {
	body := []byte(`{"error":{"code":"session_expired","message":"Bearer secret-token"},"api_key":"secret-token"}`)
	message := freebuffErrorMessage(body)
	if message != "freebuff: upstream error session_expired" {
		t.Fatalf("message = %q", message)
	}
	if strings.Contains(message, "secret-token") {
		t.Fatalf("message leaked upstream secret: %q", message)
	}
}

func TestFreebuffCleanupContextIsBoundedAndDetached(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()

	cleanupCtx, cancelCleanup := freebuffCleanupContext(parent)
	defer cancelCleanup()
	if cleanupCtx.Err() != nil {
		t.Fatalf("cleanup context inherited cancellation: %v", cleanupCtx.Err())
	}
	deadline, ok := cleanupCtx.Deadline()
	if !ok {
		t.Fatal("cleanup context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > freebuffCleanupTimeout {
		t.Fatalf("cleanup deadline remaining = %v, want within %v", remaining, freebuffCleanupTimeout)
	}
}

func TestFreebuffSessionUnknownStatusDoesNotReflectValue(t *testing.T) {
	const secretStatus = "secret-token-from-upstream"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":%q}`, secretStatus)
	}))
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeAPIKey: "token",
		"base_url":                   server.URL,
	}}
	_, _, err := executor.sessionRequest(context.Background(), auth, http.MethodGet, "model", "")
	if err == nil {
		t.Fatal("sessionRequest error = nil, want unknown-status failure")
	}
	if strings.Contains(err.Error(), secretStatus) {
		t.Fatalf("unknown session status leaked upstream value: %q", err.Error())
	}
}

func TestFreebuffSessionUsesCanonicalUserAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != freebuffUserAgent {
			t.Fatalf("User-Agent = %q, want %q", got, freebuffUserAgent)
		}
		_, _ = io.WriteString(w, `{"status":"none"}`)
	}))
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeAPIKey: "token", "base_url": server.URL,
	}}
	if _, _, err := executor.sessionRequest(t.Context(), auth, http.MethodGet, "model", ""); err != nil {
		t.Fatalf("sessionRequest error = %v", err)
	}
}

func TestFreebuffCredentialStateSharedByAccountIdentity(t *testing.T) {
	executor := NewFreebuffExecutor(&config.Config{})
	attributes := map[string]string{
		cliproxyauth.AttributeAPIKey: "same-token", "base_url": "https://www.codebuff.com",
	}
	first := executor.stateFor(&cliproxyauth.Auth{ID: "first-config", Attributes: attributes})
	second := executor.stateFor(&cliproxyauth.Auth{ID: "second-config", Attributes: maps.Clone(attributes)})
	if first != second {
		t.Fatal("duplicate config entries for one account received distinct credential states")
	}
}

func TestFreebuffCredentialStateUsesEffectiveBaseURLIdentity(t *testing.T) {
	executor := NewFreebuffExecutor(&config.Config{})
	auth := func(baseURL string) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{Attributes: map[string]string{
			cliproxyauth.AttributeAPIKey: "normalized-token", "base_url": baseURL,
		}}
	}
	if executor.stateFor(auth("https://EXAMPLE.com/v1")) != executor.stateFor(auth("https://example.com")) {
		t.Fatal("equivalent effective base URLs received distinct credential states")
	}
	if executor.stateFor(auth("https://example.com/TenantA")) == executor.stateFor(auth("https://example.com/tenanta")) {
		t.Fatal("case-sensitive URL paths were merged into one credential state")
	}
}

func TestFreebuffCredentialStateEvictedOnExecutorReplacement(t *testing.T) {
	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeAPIKey: "eviction-token", "base_url": "https://www.codebuff.com",
	}}
	before := executor.stateFor(auth)
	executor.CloseExecutionSession(cliproxyauth.CloseAllExecutionSessionsID)
	after := executor.stateFor(auth)
	if before == after {
		t.Fatal("idle credential state survived executor replacement cleanup")
	}
}

func TestFreebuffQueuedSessionCancellationReleasesInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var deletes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			cancel()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "queued", "model": "model-a", "instanceId": "queued-instance",
			})
		case http.MethodDelete:
			deletes++
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeAPIKey: "token", "base_url": server.URL,
	}}
	if _, err := executor.waitForSession(ctx, auth, "model-a", "queued-instance"); err == nil {
		t.Fatal("waitForSession error = nil, want cancellation")
	}
	if deletes != 1 {
		t.Fatalf("DELETE calls = %d, want 1", deletes)
	}
}

func TestRunFreebuffSessionHeartbeatRefreshesSession(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called <- struct{}{}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "active", "model": "model-a", "instanceId": "instance-a",
		})
	}))
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeAPIKey: "token", "base_url": server.URL,
	}}
	state := executor.stateFor(auth)
	state.session = &freebuffSession{model: "model-a", instanceID: "instance-a"}
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		executor.runSessionHeartbeat(ctx, auth, state, "model-a", "instance-a", ticks)
	}()
	ticks <- time.Now()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("heartbeat request was not observed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop after cancellation")
	}
}

func TestFreebuffHeartbeatDoesNotRestoreReplacedSession(t *testing.T) {
	executor := NewFreebuffExecutor(&config.Config{})
	state := &freebuffCredentialState{
		lease:   make(chan struct{}, 1),
		session: &freebuffSession{model: "model-b", instanceID: "new-instance"},
	}
	executor.applyHeartbeatSession(
		state,
		"model-a",
		"old-instance",
		&freebuffSession{model: "model-a", instanceID: "old-instance"},
	)
	if state.session.instanceID != "new-instance" {
		t.Fatalf("heartbeat restored stale session: %#v", state.session)
	}
}

func TestFreebuffScannerFailureIsTypedBadGateway(t *testing.T) {
	err := consumeFreebuffSSE(errorReader{err: errors.New("read failed")}, func([]byte, map[string]any) error {
		return nil
	})
	var typed interface{ StatusCode() int }
	if !errors.As(err, &typed) || typed.StatusCode() != http.StatusBadGateway {
		t.Fatalf("error = %#v, want typed 502", err)
	}
}

func TestFreebuffScannerPreservesCancellation(t *testing.T) {
	err := consumeFreebuffSSE(errorReader{err: context.Canceled}, func([]byte, map[string]any) error {
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %#v, want context.Canceled", err)
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestConsumeFreebuffSSERejectsTruncatedStream(t *testing.T) {
	err := consumeFreebuffSSE(strings.NewReader(`data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n"), func(_ []byte, _ map[string]any) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("consumeFreebuffSSE() error = %v, want truncation", err)
	}
}

func TestFreebuffExecutorStreamLifecycleAgainstMockUpstream(t *testing.T) {
	var runActions []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/freebuff/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "active", "model": "deepseek/deepseek-v4-flash", "instanceId": "instance-1",
			})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/agent-runs", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		action, _ := body["action"].(string)
		runActions = append(runActions, action)
		if action == "START" {
			_ = json.NewEncoder(w).Encode(map[string]any{"runId": "run-1"})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	})
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"id":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}` + "\n\n" +
				`data: {"id":"chat-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n",
		))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "freebuff-test",
		Attributes: map[string]string{
			"api_key":  "token",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: req.Payload,
	}
	result, err := executor.ExecuteStream(t.Context(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var chunks int
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		chunks++
	}
	if chunks == 0 {
		t.Fatal("ExecuteStream() emitted no chunks")
	}
	if strings.Join(runActions, ",") != "START,FINISH" {
		t.Fatalf("run actions = %v, want START,FINISH", runActions)
	}
}

func TestFreebuffExecutorRetriesOneStaleSessionBeforeOutput(t *testing.T) {
	var sessionPosts, starts, finishes, chats int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/freebuff/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sessionPosts++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "active", "model": "deepseek/deepseek-v4-flash",
			"instanceId": fmt.Sprintf("instance-%d", sessionPosts),
		})
	})
	mux.HandleFunc("/api/v1/agent-runs", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["action"] == "START" {
			starts++
			_ = json.NewEncoder(w).Encode(map[string]any{"runId": fmt.Sprintf("run-%d", starts)})
			return
		}
		finishes++
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		chats++
		if chats == 1 {
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"error":"session_expired"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "freebuff-stale-chat", Attributes: map[string]string{
		"api_key": "token", "base_url": server.URL,
	}}
	result, err := executor.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}
	if chats != 2 || starts != 2 || finishes != 2 || sessionPosts != 2 {
		t.Fatalf("chats=%d starts=%d finishes=%d sessionPosts=%d", chats, starts, finishes, sessionPosts)
	}
}

func TestFreebuffHTTPErrorTreatsNotFoundAsRequestScoped(t *testing.T) {
	t.Parallel()

	err := freebuffHTTPError(http.StatusNotFound, []byte(`{"error":"unknown tool"}`), nil)
	if err == nil {
		t.Fatal("freebuffHTTPError() = nil")
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("StatusCode() = %v, want 400", err)
	}
	scoped, ok := errors.AsType[cliproxyexecutor.RequestScopedError](err)
	if !ok || scoped == nil || !scoped.IsRequestScoped() {
		t.Fatalf("IsRequestScoped() = %v %v, want true", ok, err)
	}
	if strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("error leaked upstream body: %q", err)
	}

	authErr := freebuffHTTPError(http.StatusUnauthorized, nil, nil)
	if authScoped, authOK := errors.AsType[cliproxyexecutor.RequestScopedError](authErr); authOK && authScoped != nil && authScoped.IsRequestScoped() {
		t.Fatal("401 must keep credential cooldown")
	}
}

func TestFreebuffExecutorChatNotFoundIsRequestScoped(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/freebuff/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "active", "model": "deepseek/deepseek-v4-flash", "instanceId": "instance-404",
		})
	})
	mux.HandleFunc("/api/v1/agent-runs", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["action"] == "START" {
			_ = json.NewEncoder(w).Encode(map[string]any{"runId": "run-404"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"unknown tool get_weather"}}`, http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "freebuff-chat-404", Attributes: map[string]string{
		"api_key": "token", "base_url": server.URL,
	}}
	_, err := executor.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"weather?"}],"stream":true,"tools":[{"type":"function","function":{"name":"get_weather"}}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err == nil {
		t.Fatal("ExecuteStream() error = nil, want request-scoped 400")
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("ExecuteStream() status = %v, want 400", err)
	}
	scoped, ok := errors.AsType[cliproxyexecutor.RequestScopedError](err)
	if !ok || scoped == nil || !scoped.IsRequestScoped() {
		t.Fatalf("ExecuteStream() IsRequestScoped() = %v %v, want true", ok, err)
	}
}

func TestFreebuffFinishRunTreatsMissingRunAsSuccess(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent-runs", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"run not found"}`, http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "freebuff-finish-404", Attributes: map[string]string{
		"api_key": "token", "base_url": server.URL,
	}}
	err := executor.finishRun(t.Context(), auth, &freebuffRun{id: "run-gone", startedAt: time.Now()}, "completed", "chatcmpl-1", nil)
	if err != nil {
		t.Fatalf("finishRun() error = %v, want nil for missing run", err)
	}
}

func TestFreebuffFinishRunRetriesServerErrorOnce(t *testing.T) {
	t.Parallel()

	var finishes atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent-runs", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["action"] != "FINISH" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if finishes.Add(1) == 1 {
			http.Error(w, `{"error":"temporary"}`, http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "freebuff-finish-retry", Attributes: map[string]string{
		"api_key": "token", "base_url": server.URL,
	}}
	err := executor.finishRun(t.Context(), auth, &freebuffRun{id: "run-retry", startedAt: time.Now()}, "completed", "chatcmpl-2", nil)
	if err != nil {
		t.Fatalf("finishRun() error = %v, want nil after 502 retry", err)
	}
	if got := finishes.Load(); got != 2 {
		t.Fatalf("FINISH attempts = %d, want 2", got)
	}
}

func TestFreebuffResolveModelAcceptsProviderAliasAndShortNames(t *testing.T) {
	executor := NewFreebuffExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "token"}}
	cases := []struct {
		in, model, agent string
	}{
		{"freebuff", "deepseek/deepseek-v4-pro", "base2-free-deepseek"},
		{"codebuff", "deepseek/deepseek-v4-pro", "base2-free-deepseek"},
		{"deepseek-v4-pro", "deepseek/deepseek-v4-pro", "base2-free-deepseek"},
		{"deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-flash", "base2-free-deepseek-flash"},
		{"claude-fable-5", "anthropic/claude-fable-5", "base2-free-fable"},
		{"openai/gpt-5.6-luna", "openai/gpt-5.6-luna", "base2-free-luna"},
	}
	for _, tc := range cases {
		model, agentID := executor.resolveModel(auth, tc.in)
		if model != tc.model || agentID != tc.agent {
			t.Fatalf("resolveModel(%q) = %q, %q, want %q, %q", tc.in, model, agentID, tc.model, tc.agent)
		}
	}
}

func TestFreebuffResolveModelFillsMissingConfigAgentID(t *testing.T) {
	executor := NewFreebuffExecutor(&config.Config{FreebuffKey: []config.FreebuffKey{{
		APIKey: "token",
		Models: []config.FreebuffModel{{Name: "freebuff", Alias: "freebuff"}},
	}}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "token"}}
	model, agentID := executor.resolveModel(auth, "freebuff")
	if model != "deepseek/deepseek-v4-pro" || agentID != "base2-free-deepseek" {
		t.Fatalf("resolveModel() = %q, %q", model, agentID)
	}
}

func TestFreebuffResolveModelKeepsExplicitConfigAgentID(t *testing.T) {
	executor := NewFreebuffExecutor(&config.Config{FreebuffKey: []config.FreebuffKey{{
		APIKey: "token",
		Models: []config.FreebuffModel{{Name: "custom-model", Alias: "freebuff", AgentID: "custom-agent"}},
	}}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "token"}}
	model, agentID := executor.resolveModel(auth, "freebuff")
	if model != "custom-model" || agentID != "custom-agent" {
		t.Fatalf("resolveModel() = %q, %q", model, agentID)
	}
}

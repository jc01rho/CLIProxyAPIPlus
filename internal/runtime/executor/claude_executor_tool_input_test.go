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

type claudeToolInputCapture struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *claudeToolInputCapture) last() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) == 0 {
		return nil
	}
	return c.bodies[len(c.bodies)-1]
}

func newClaudeToolInputServer(t *testing.T, capture *claudeToolInputCapture) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.mu.Lock()
		capture.bodies = append(capture.bodies, body)
		capture.mu.Unlock()
		if gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func claudeToolInputPayload(inputField string) []byte {
	return []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[` +
		`{"role":"user","content":"run it"},` +
		`{"role":"assistant","content":[{"type":"text","text":"calling"},{"type":"tool_use","id":"toolu_1","name":"Bash"` + inputField + `}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}]}`)
}

func claudeToolInputSent(t *testing.T, body []byte) gjson.Result {
	t.Helper()
	var found gjson.Result
	gjson.GetBytes(body, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_use" && block.Get("id").String() == "toolu_1" {
				found = block
				return false
			}
			return true
		})
		return !found.Exists()
	})
	if !found.Exists() {
		t.Fatalf("tool_use block missing from upstream body: %s", body)
	}
	return found.Get("input")
}

func runClaudeToolInputBothModes(t *testing.T, cfg *config.Config, payload []byte) (nonStream, stream []byte) {
	t.Helper()
	capture := &claudeToolInputCapture{}
	server := newClaudeToolInputServer(t, capture)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123", "base_url": server.URL}}
	executor := NewClaudeExecutor(cfg)
	req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	nonStream = capture.last()

	result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	stream = capture.last()
	return nonStream, stream
}

func TestClaudeExecutor_NormalizesNativeToolUseInputForUpstream(t *testing.T) {
	tests := []struct {
		name       string
		inputField string
		wantRaw    string
		wantType   gjson.Type
	}{
		{name: "object preserved", inputField: `,"input":{"command":"ls","opts":{"all":true}}`, wantRaw: `{"command":"ls","opts":{"all":true}}`},
		{name: "json string object decoded", inputField: `,"input":"{\"command\":\"ls -la\",\"n\":2}"`, wantRaw: `{"command":"ls -la","n":2}`},
		{name: "json string object with whitespace decoded", inputField: `,"input":" {\"command\":\"pwd\"} "`, wantRaw: `{"command":"pwd"}`},
		{name: "null becomes empty object", inputField: `,"input":null`, wantRaw: `{}`},
		{name: "missing becomes empty object", inputField: ``, wantRaw: `{}`},
		{name: "empty string becomes empty object", inputField: `,"input":""`, wantRaw: `{}`},
		// Meaningful non-object values are never discarded into {}.
		{name: "array untouched", inputField: `,"input":[1,"two"]`, wantRaw: `[1,"two"]`},
		{name: "number untouched", inputField: `,"input":42`, wantRaw: `42`},
		{name: "boolean untouched", inputField: `,"input":true`, wantRaw: `true`},
		{name: "plain string untouched", inputField: `,"input":"ls -la"`, wantRaw: `"ls -la"`},
		{name: "json string array untouched", inputField: `,"input":"[1,2]"`, wantRaw: `"[1,2]"`},
		{name: "truncated json string untouched", inputField: `,"input":"{\"command\":\"ls"`, wantRaw: `"{\"command\":\"ls"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nonStream, stream := runClaudeToolInputBothModes(t, &config.Config{}, claudeToolInputPayload(tc.inputField))
			gotNonStream := claudeToolInputSent(t, nonStream)
			gotStream := claudeToolInputSent(t, stream)
			if gotNonStream.Raw != tc.wantRaw {
				t.Fatalf("non-stream tool_use.input = %s, want %s", gotNonStream.Raw, tc.wantRaw)
			}
			if gotStream.Raw != gotNonStream.Raw {
				t.Fatalf("stream tool_use.input = %s, non-stream = %s", gotStream.Raw, gotNonStream.Raw)
			}
			if gotMessages, wantMessages := gjson.GetBytes(stream, "messages").Raw, gjson.GetBytes(nonStream, "messages").Raw; gotMessages != wantMessages {
				t.Fatalf("stream/non-stream messages differ:\nstream=%s\nnonstream=%s", gotMessages, wantMessages)
			}
		})
	}
}

func TestClaudeExecutor_ToolUseInputNormalizationPrecedesPayloadConfig(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{OverrideRaw: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "claude-opus-5-5"}},
		Params: map[string]any{"messages.1.content.1.input": `"configured-string"`},
	}}}}
	nonStream, stream := runClaudeToolInputBothModes(t, cfg, claudeToolInputPayload(`,"input":null`))
	for name, body := range map[string][]byte{"non-stream": nonStream, "stream": stream} {
		if got := claudeToolInputSent(t, body).Raw; got != `"configured-string"` {
			t.Fatalf("%s: payload override must win over tool input normalization, got %s", name, got)
		}
	}
}

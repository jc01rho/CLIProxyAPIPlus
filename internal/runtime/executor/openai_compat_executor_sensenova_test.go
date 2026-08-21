package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestIsSensenovaCompatProvider(t *testing.T) {
	cases := []struct {
		name   string
		compat *config.OpenAICompatibility
		want   bool
	}{
		{name: "nil compat", compat: nil, want: false},
		{name: "exact name", compat: &config.OpenAICompatibility{Name: "sensenova"}, want: true},
		{name: "mixed case substring", compat: &config.OpenAICompatibility{Name: "My-SenseNova-Proxy"}, want: true},
		{name: "other provider", compat: &config.OpenAICompatibility{Name: "nvidia-nvapi"}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSensenovaCompatProvider(tc.compat); got != tc.want {
				t.Fatalf("isSensenovaCompatProvider(%v) = %v, want %v", tc.compat, got, tc.want)
			}
		})
	}
}

func TestApplySensenovaMaxTokensClamp(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "above ceiling clamps to 65536",
			body: `{"model":"m","max_tokens":70000}`,
			want: `{"model":"m","max_tokens":65536}`,
		},
		{
			name: "zero clamps to 1",
			body: `{"model":"m","max_tokens":0}`,
			want: `{"model":"m","max_tokens":1}`,
		},
		{
			name: "negative clamps to 1",
			body: `{"model":"m","max_tokens":-5}`,
			want: `{"model":"m","max_tokens":1}`,
		},
		{
			name: "in range is unchanged",
			body: `{"model":"m","max_tokens":4096}`,
			want: `{"model":"m","max_tokens":4096}`,
		},
		{
			name: "ceiling boundary is unchanged",
			body: `{"model":"m","max_tokens":65536}`,
			want: `{"model":"m","max_tokens":65536}`,
		},
		{
			name: "floor boundary is unchanged",
			body: `{"model":"m","max_tokens":1}`,
			want: `{"model":"m","max_tokens":1}`,
		},
		{
			name: "absent max_tokens is unchanged",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			want: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "null max_tokens is left to upstream default",
			body: `{"model":"m","max_tokens":null}`,
			want: `{"model":"m","max_tokens":null}`,
		},
		{
			name: "invalid json is returned untouched",
			body: `not json`,
			want: `not json`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(applySensenovaMaxTokensClamp([]byte(tc.body)))
			if got != tc.want {
				t.Fatalf("applySensenovaMaxTokensClamp() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSanitizeSensenovaToolCalls(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty function name is dropped",
			body: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"","arguments":"{\"x\":1}"}},` +
				`{"id":"b","type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]}]}`,
			want: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"b","type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]}]}`,
		},
		{
			name: "whitespace function name is dropped",
			body: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"   ","arguments":"{}"}},` +
				`{"id":"b","type":"function","function":{"name":"keep","arguments":"{}"}}]}]}`,
			want: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"b","type":"function","function":{"name":"keep","arguments":"{}"}}]}]}`,
		},
		{
			name: "empty arguments are filled with empty object",
			body: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"keep","arguments":""}}]}]}`,
			want: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"keep","arguments":"{}"}}]}]}`,
		},
		{
			name: "missing arguments are filled with empty object",
			body: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"keep"}}]}]}`,
			want: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"keep","arguments":"{}"}}]}]}`,
		},
		{
			name: "whitespace arguments are filled with empty object",
			body: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"keep","arguments":"  "}}]}]}`,
			want: `{"messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"keep","arguments":"{}"}}]}]}`,
		},
		{
			name: "message losing every tool call loses the field",
			body: `{"messages":[{"role":"assistant","content":"hi","tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"","arguments":"{}"}}]}]}`,
			want: `{"messages":[{"role":"assistant","content":"hi"}]}`,
		},
		{
			name: "orphaned tool result for dropped call is removed",
			body: `{"messages":[` +
				`{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"a","content":"orphan"}]}`,
			want: `{"messages":[` +
				`{"role":"assistant"}]}`,
		},
		{
			name: "valid tool calls and sibling fields are preserved",
			body: `{"model":"m","messages":[` +
				`{"role":"user","content":"hi"},` +
				`{"role":"assistant","content":null,"tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]},` +
				`{"role":"tool","tool_call_id":"a","content":"ok"}]}`,
			want: `{"model":"m","messages":[` +
				`{"role":"user","content":"hi"},` +
				`{"role":"assistant","content":null,"tool_calls":[` +
				`{"id":"a","type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]},` +
				`{"role":"tool","tool_call_id":"a","content":"ok"}]}`,
		},
		{
			name: "repairs only the offending message",
			body: `{"messages":[` +
				`{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]},` +
				`{"role":"assistant","tool_calls":[{"id":"b","type":"function","function":{"name":"","arguments":"{}"}}]}]}`,
			want: `{"messages":[` +
				`{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]},` +
				`{"role":"assistant"}]}`,
		},
		{
			name: "no messages array is unchanged",
			body: `{"model":"m","max_tokens":10}`,
			want: `{"model":"m","max_tokens":10}`,
		},
		{
			name: "invalid json is returned untouched",
			body: `not json`,
			want: `not json`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeSensenovaToolCalls([]byte(tc.body))
			assertJSONEqual(t, got, []byte(tc.want))
		})
	}
}

// assertJSONEqual compares two payloads by decoded structure so the assertion
// tracks the contract rather than key ordering or whitespace. Non-JSON inputs
// fall back to byte equality, which is what the passthrough cases assert.
func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		if string(got) != string(want) {
			t.Fatalf("got %s, want %s", got, want)
		}
		return
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("got invalid JSON %s: %v", got, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func newSensenovaExecutor(t *testing.T, handler http.HandlerFunc) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "sensenova",
			BaseURL: server.URL + "/v1",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "sensenova",
	}}
	return executor, auth
}

const sensenovaDirtyPayload = `{"model":"SenseChat-5","messages":[` +
	`{"role":"user","content":"hi"},` +
	`{"role":"assistant","tool_calls":[` +
	`{"id":"a","type":"function","function":{"name":"","arguments":"{}"}},` +
	`{"id":"b","type":"function","function":{"name":"keep","arguments":""}}]}],` +
	`"max_tokens":70000}`

func assertSensenovaNormalized(t *testing.T, gotBody []byte) {
	t.Helper()
	if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 65536 {
		t.Fatalf("max_tokens = %d, want 65536; body=%s", got, gotBody)
	}
	calls := gjson.GetBytes(gotBody, "messages.1.tool_calls")
	if !calls.IsArray() || len(calls.Array()) != 1 {
		t.Fatalf("tool_calls = %s, want exactly the named call; body=%s", calls.Raw, gotBody)
	}
	call := calls.Array()[0]
	if name := call.Get("function.name").String(); name != "keep" {
		t.Fatalf("function.name = %q, want %q; body=%s", name, "keep", gotBody)
	}
	if args := call.Get("function.arguments").String(); args != "{}" {
		t.Fatalf("function.arguments = %q, want %q; body=%s", args, "{}", gotBody)
	}
}

func TestOpenAICompatExecutor_SensenovaNormalizesRequest(t *testing.T) {
	var gotBody []byte
	executor, auth := newSensenovaExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	})

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "SenseChat-5",
		Payload: []byte(sensenovaDirtyPayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	assertSensenovaNormalized(t, gotBody)
}

func TestOpenAICompatExecutor_SensenovaNormalizesStreamRequest(t *testing.T) {
	var gotBody []byte
	executor, auth := newSensenovaExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "SenseChat-5",
		Payload: []byte(sensenovaDirtyPayload),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	drainStreamChunks(t, stream.Chunks)

	assertSensenovaNormalized(t, gotBody)
}

func TestOpenAICompatExecutor_NonSensenovaLeavesRequestUnchanged(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "other-provider",
			BaseURL: server.URL + "/v1",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "other-provider",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "some-model",
		Payload: []byte(`{"model":"some-model","messages":[{"role":"user","content":"hi"}],"max_tokens":70000}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 70000 {
		t.Fatalf("max_tokens = %d, want 70000; body=%s", got, gotBody)
	}
}

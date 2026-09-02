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
	"github.com/tidwall/gjson"
)

func TestSanitizeEmptyToolFunctionNames(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty function name is dropped",
			body: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"","arguments":"{\"x\":1}"}},` +
				`{"type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]}`,
		},
		{
			name: "whitespace function name is dropped",
			body: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"   ","arguments":"{}"}},` +
				`{"type":"function","function":{"name":"keep","arguments":"{}"}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"{}"}}]}`,
		},
		{
			name: "empty arguments are filled with empty object",
			body: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":""}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"{}"}}]}`,
		},
		{
			name: "missing arguments are filled with empty object",
			body: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep"}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"{}"}}]}`,
		},
		{
			name: "whitespace arguments are filled with empty object",
			body: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"  "}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"{}"}}]}`,
		},
		{
			name: "request losing every tool entry loses the field",
			body: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"","arguments":"{}"}}]}`,
			want: `{"model":"m"}`,
		},
		{
			name: "valid tool entries and sibling fields are preserved",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]}`,
			want: `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]}`,
		},
		{
			name: "mixed valid and empty entries keep only valid ones",
			body: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}},` +
				`{"type":"function","function":{"name":"","arguments":"{}"}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"keep","arguments":"{\"x\":1}"}}]}`,
		},
		{
			name: "tools array with empty entries only drops the array",
			body: `{"model":"m","tools":[` +
				`{"type":"function","function":{"name":"","arguments":"{}"}},` +
				`{"type":"function","function":{"name":"   ","arguments":"{}"}}]}`,
			want: `{"model":"m"}`,
		},
		{
			name: "no tools array is unchanged",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			want: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "invalid json is returned untouched",
			body: `not json`,
			want: `not json`,
		},
		{
			name: "flat tools[].name empty is dropped, valid flat sibling kept",
			body: `{"model":"m","tools":[` +
				`{"type":"function","name":"","parameters":{}},` +
				`{"type":"function","name":"keep","parameters":{}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"function","name":"keep","parameters":{}}]}`,
		},
		{
			name: "flat tools[].name empty with sibling function object lacking name is dropped",
			body: `{"model":"m","tools":[` +
				`{"type":"function","name":"","function":{"description":"d"}},` +
				`{"type":"function","name":"keep","parameters":{}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"function","name":"keep","parameters":{}}]}`,
		},
		{
			name: "all flat tools empty deletes the tools field without collateral damage check needed",
			body: `{"model":"m","tools":[` +
				`{"type":"function","name":"","parameters":{}},` +
				`{"type":"function","name":"   ","parameters":{}}]}`,
			want: `{"model":"m"}`,
		},
		{
			name: "custom.name empty is dropped, custom.name populated is kept",
			body: `{"model":"m","tools":[` +
				`{"type":"custom","custom":{"name":""}},` +
				`{"type":"custom","custom":{"name":"keep"}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"custom","custom":{"name":"keep"}}]}`,
		},
		{
			name: "valid flat tool does not gain a function.arguments backfill",
			body: `{"model":"m","tools":[` +
				`{"type":"function","name":"","parameters":{}},` +
				`{"type":"function","name":"keep","parameters":{"x":1}}]}`,
			want: `{"model":"m","tools":[` +
				`{"type":"function","name":"keep","parameters":{"x":1}}]}`,
		},
		{
			name: "messages[].tool_calls[].function.name empty is dropped, valid sibling call kept",
			body: `{"model":"m","messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","function":{"name":"","arguments":"{}"}},` +
				`{"id":"b","function":{"name":"keep","arguments":"{}"}}]}]}`,
			want: `{"model":"m","messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"b","function":{"name":"keep","arguments":"{}"}}]}]}`,
		},
		{
			name: "messages[].tool_calls[] entirely emptied drops the field",
			body: `{"model":"m","messages":[{"role":"assistant","tool_calls":[` +
				`{"id":"a","function":{"name":"","arguments":"{}"}}]}]}`,
			want: `{"model":"m","messages":[{"role":"assistant"}]}`,
		},
		{
			name: "responses input function_call with empty name is dropped, valid sibling kept",
			body: `{"model":"m","input":[` +
				`{"type":"function_call","call_id":"c1","name":"","arguments":"{}"},` +
				`{"type":"function_call","call_id":"c2","name":"keep","arguments":"{}"}]}`,
			want: `{"model":"m","input":[` +
				`{"type":"function_call","call_id":"c2","name":"keep","arguments":"{}"}]}`,
		},
		{
			name: "responses input with only an empty-name function_call deletes input",
			body: `{"model":"m","input":[` +
				`{"type":"function_call","call_id":"c1","name":"","arguments":"{}"}]}`,
			want: `{"model":"m"}`,
		},
		{
			name: "responses input non function_call items are left untouched",
			body: `{"model":"m","input":[` +
				`{"type":"message","role":"user","content":"hi"}]}`,
			want: `{"model":"m","input":[` +
				`{"type":"message","role":"user","content":"hi"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeEmptyToolFunctionNames([]byte(tc.body))
			assertJSONEqual(t, got, []byte(tc.want))
		})
	}
}

func TestOpenAICompatExecutor_DropsEmptyToolFunctionNamesRedirect(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "minpeter",
			BaseURL: server.URL + "/v1",
		}},
	})

	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url":    server.URL + "/v1",
			"api_key":     "test",
			"compat_name": "minpeter",
		},
	}, cliproxyexecutor.Request{
		Model: "solar-pro-4",
		Payload: []byte(`{"model":"solar-pro-4","messages":[{"role":"user","content":"hi"}],"tools":[
{"type":"function","function":{"name":"","arguments":"{}"}},
{"type":"function","function":{"name":"keep","arguments":""}}
]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	calls := gjson.GetBytes(gotBody, "tools")
	if !calls.IsArray() || len(calls.Array()) != 1 {
		t.Fatalf("tools = %s, want exactly the named call; body=%s", calls.Raw, gotBody)
	}
	call := calls.Array()[0]
	if name := call.Get("function.name").String(); name != "keep" {
		t.Fatalf("function.name = %q, want %q; body=%s", name, "keep", gotBody)
	}
	if args := call.Get("function.arguments").String(); args != "{}" {
		t.Fatalf("function.arguments = %q, want %q; body=%s", args, "{}", gotBody)
	}
}

func TestOpenAICompatExecutor_DropsEmptyToolFunctionNamesStreamRedirect(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "minpeter",
			BaseURL: server.URL + "/v1",
		}},
	})

	stream, err := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url":    server.URL + "/v1",
			"api_key":     "test",
			"compat_name": "minpeter",
		},
	}, cliproxyexecutor.Request{
		Model: "solar-pro-4",
		Payload: []byte(`{"model":"solar-pro-4","messages":[{"role":"user","content":"hi"}],"tools":[
{"type":"function","function":{"name":"","arguments":"{}"}},
{"type":"function","function":{"name":"keep","arguments":""}}
]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	drainStreamChunks(t, stream.Chunks)

	calls := gjson.GetBytes(gotBody, "tools")
	if !calls.IsArray() || len(calls.Array()) != 1 {
		t.Fatalf("tools = %s, want exactly the named call; body=%s", calls.Raw, gotBody)
	}
	call := calls.Array()[0]
	if name := call.Get("function.name").String(); name != "keep" {
		t.Fatalf("function.name = %q, want %q; body=%s", name, "keep", gotBody)
	}
	if args := call.Get("function.arguments").String(); args != "{}" {
		t.Fatalf("function.arguments = %q, want %q; body=%s", args, "{}", gotBody)
	}
}

func TestOpenAICompatExecutor_EmptyToolFunctionNamesDroppedForAllProviders(t *testing.T) {
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

	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url":    server.URL + "/v1",
			"api_key":     "test",
			"compat_name": "other-provider",
		},
	}, cliproxyexecutor.Request{
		Model: "solar-pro",
		Payload: []byte(`{"model":"solar-pro","messages":[{"role":"user","content":"hi"}],"tools":[
{"type":"function","function":{"name":"","arguments":"{}"}}
]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Empty-name tools are dropped for ALL providers since sanitization is universal.
	calls := gjson.GetBytes(gotBody, "tools")
	if calls.Exists() {
		t.Fatalf("tools = %s, want tools field absent (empty-name entry should be dropped for any provider); body=%s", calls.Raw, gotBody)
	}
}

// assertJSONEqual lives in openai_compat_executor_sensenova_test.go.

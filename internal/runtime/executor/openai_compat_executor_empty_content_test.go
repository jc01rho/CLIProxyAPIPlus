package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestSanitizeEmptyMessageContent(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "user with empty content array is dropped",
			body: `{"messages":[{"role":"user","content":[]},{"role":"user","content":"hi"}]}`,
			want: `{"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "user with null content is dropped",
			body: `{"messages":[{"role":"user","content":null},{"role":"user","content":"hi"}]}`,
			want: `{"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "system with missing content is dropped",
			body: `{"messages":[{"role":"system"},{"role":"user","content":"hi"}]}`,
			want: `{"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "assistant with empty array and tool_calls gets null content",
			body: `{"messages":[{"role":"assistant","content":[],"tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`,
			want: `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`,
		},
		{
			name: "assistant with missing content and tool_calls is untouched",
			body: `{"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`,
			want: `{"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`,
		},
		{
			name: "assistant with only reasoning gets empty string content",
			body: `{"messages":[{"role":"assistant","content":[],"reasoning_content":"thinking"}]}`,
			want: `{"messages":[{"role":"assistant","content":"","reasoning_content":"thinking"}]}`,
		},
		{
			name: "assistant with nothing is dropped",
			body: `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[]},{"role":"user","content":"again"}]}`,
			want: `{"messages":[{"role":"user","content":"hi"},{"role":"user","content":"again"}]}`,
		},
		{
			name: "tool result with empty array keeps its pairing with empty string",
			body: `{"messages":[{"role":"tool","tool_call_id":"a","content":[]}]}`,
			want: `{"messages":[{"role":"tool","tool_call_id":"a","content":""}]}`,
		},
		{
			name: "valid content shapes are untouched",
			body: `{"messages":[{"role":"user","content":""},{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":null,"tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":""}]}`,
			want: `{"messages":[{"role":"user","content":""},{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":null,"tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":""}]}`,
		},
		{
			name: "several invalid messages are all repaired with stable indexes",
			body: `{"messages":[{"role":"user","content":[]},{"role":"user","content":"a"},{"role":"user","content":[]},{"role":"user","content":"b"},{"role":"user","content":null}]}`,
			want: `{"messages":[{"role":"user","content":"a"},{"role":"user","content":"b"}]}`,
		},
		{
			name: "no messages array is unchanged",
			body: `{"model":"m"}`,
			want: `{"model":"m"}`,
		},
		{
			name: "invalid json is returned untouched",
			body: `not json`,
			want: `not json`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeEmptyMessageContent([]byte(tc.body))
			assertJSONEqual(t, got, []byte(tc.want))
		})
	}
}

// A Responses input message whose content array is empty is translated to a
// chat message with `content: []`, which strict upstreams reject with 400.
func TestOpenAICompatExecutor_RepairsEmptyMessageContentFromResponsesInput(t *testing.T) {
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
		Model: "beam-free",
		Payload: []byte(`{"model":"beam-free","input":[
{"type":"message","role":"user","content":[]},
{"type":"message","role":"assistant","content":[]},
{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	assertNoEmptyMessageContent(t, gotBody)
}

func TestOpenAICompatExecutor_RepairsEmptyMessageContentFromResponsesInputStream(t *testing.T) {
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
		Model: "beam-free",
		Payload: []byte(`{"model":"beam-free","input":[
{"type":"message","role":"user","content":[]},
{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range stream.Chunks {
	}

	assertNoEmptyMessageContent(t, gotBody)
}

func assertNoEmptyMessageContent(t *testing.T, body []byte) {
	t.Helper()
	msgs := gjson.GetBytes(body, "messages").Array()
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want only the non-empty user message; body=%s", len(msgs), body)
	}
	content := msgs[0].Get("content")
	if msgs[0].Get("role").String() != "user" || !content.IsArray() || len(content.Array()) != 1 {
		t.Fatalf("unexpected surviving message %s; body=%s", msgs[0].Raw, body)
	}
}

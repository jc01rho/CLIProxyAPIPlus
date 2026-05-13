package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorCompactPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"gpt-5.1-codex-max","input":[{"role":"user","content":"hi"}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.1-codex-max",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses/compact")
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected input in body")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected messages in body")
	}
	if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutor_NvidiaCompatReducesMaxTokens(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "nvidia-nvapi",
			BaseURL: server.URL + "/v1",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "nvidia-nvapi",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-ai/deepseek-v3.2",
		Payload: []byte(`{"model":"deepseek-ai/deepseek-v3.2","messages":[{"role":"user","content":"hi"}],"max_tokens":32000}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 31998 {
		t.Fatalf("max_tokens = %d, want %d", got, 31998)
	}
}

func TestOpenAICompatExecutorPayloadOverrideWinsOverThinkingSuffix(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "custom-openai", Protocol: "openai"},
					},
					Params: map[string]any{
						"reasoning_effort": "low",
					},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"custom-openai(high)","messages":[{"role":"user","content":"hi"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-openai(high)",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, "reasoning_effort").String(); got != "low" {
		t.Fatalf("reasoning_effort = %q, want %q; body=%s", got, "low", string(gotBody))
	}
}

func TestOpenAICompatExecutorFillsReasoningContentForToolCallsWhenReasoningEnabled(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"moonshotai/kimi-k2","reasoning_effort":"high","messages":[{"role":"assistant","content":"previous","reasoning_content":"previous reasoning"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list","arguments":"{}"}}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "moonshotai/kimi-k2",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "messages.1.reasoning_content").String(); got != "previous reasoning" {
		t.Fatalf("messages.1.reasoning_content = %q, want previous reasoning; body=%s", got, string(gotBody))
	}
}

func TestOpenAICompatExecutorSkipsReasoningContentWithoutReasoningSignal(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"generic-compatible","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list","arguments":"{}"}}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "generic-compatible",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if reasoning := gjson.GetBytes(gotBody, "messages.0.reasoning_content"); reasoning.Exists() {
		t.Fatalf("messages.0.reasoning_content should not be added without reasoning signal; body=%s", string(gotBody))
	}
}

func TestOpenAICompatExecutorStripsReasoningReplayForMistralProvider(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("mistral.ai", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "mistral.ai", Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"mistral-medium","messages":[{"role":"assistant","content":"previous","reasoning_content":"previous reasoning"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list","arguments":"{}"}}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mistral-medium",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	for _, path := range []string{"messages.0.reasoning_content", "messages.1.reasoning_content"} {
		if value := gjson.GetBytes(gotBody, path); value.Exists() {
			t.Fatalf("%s should be stripped for mistral.ai; body=%s", path, string(gotBody))
		}
	}
}

func TestOpenAICompatExecutorForcesReasoningReplayForXiaomiProvider(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("xiaomi", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "xiaomi", Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"mi-thinking","messages":[{"role":"assistant","content":"previous","reasoning_content":"chain reasoning"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list","arguments":"{}"}}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mi-thinking",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "messages.1.reasoning_content").String(); got != "chain reasoning" {
		t.Fatalf("messages.1.reasoning_content = %q, want chain reasoning; body=%s", got, string(gotBody))
	}
}

func TestOpenAICompatExecutorBackfillsReasoningReplayForXiaomiProviderWithoutExistingChain(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("xiaomi", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "xiaomi", Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"mi-thinking","thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":"Need to call tool","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list","arguments":"{}"}}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mi-thinking",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "messages.0.reasoning_content").String(); got != "Need to call tool" {
		t.Fatalf("messages.0.reasoning_content = %q, want fallback content; body=%s", got, string(gotBody))
	}
}

func TestOpenAICompatExecutorStripsUnsupportedMistralTopLevelFields(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("mistral.ai", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "mistral.ai", Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"mistral-medium-latest","thinking":{"type":"enabled","budgetTokens":8192},"interleaved":{"field":"reasoning_content"},"reasoning":{"effort":"high"},"reasoningSummary":"auto","include":["reasoning.encrypted_content"],"verbosity":"low","messages":[{"role":"assistant","content":"previous","reasoning_content":"private chain"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list","arguments":"{}"}}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mistral-medium",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	for _, path := range []string{"reasoning", "reasoningSummary", "include", "verbosity", "thinking", "interleaved"} {
		if value := gjson.GetBytes(gotBody, path); value.Exists() {
			t.Fatalf("%s should be stripped for mistral.ai; body=%s", path, string(gotBody))
		}
	}
	for _, path := range []string{"messages.0.reasoning_content", "messages.1.reasoning_content"} {
		if value := gjson.GetBytes(gotBody, path); value.Exists() {
			t.Fatalf("%s should be stripped for mistral.ai; body=%s", path, string(gotBody))
		}
	}
}

func TestOpenAICompatExecutorStripsUnsupportedMistralTopLevelFieldsInStream(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("mistral.ai", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "mistral.ai", Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"mistral-medium-latest","stream":true,"thinking":{"type":"enabled","budgetTokens":8192},"interleaved":{"field":"reasoning_content"},"messages":[{"role":"user","content":"hi"}]}`)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mistral-medium-latest",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}

	for _, path := range []string{"thinking", "interleaved"} {
		if value := gjson.GetBytes(gotBody, path); value.Exists() {
			t.Fatalf("%s should be stripped for mistral.ai stream; body=%s", path, string(gotBody))
		}
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "mistral-medium-latest" {
		t.Fatalf("model = %q, want %q; body=%s", got, "mistral-medium-latest", string(gotBody))
	}
}

func TestOpenAICompatExecutor_NonMistralKeepsTopLevelCompatibilityFields(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"generic-compatible","reasoning":{"effort":"high"},"reasoningSummary":"auto","include":["reasoning.encrypted_content"],"verbosity":"low","messages":[{"role":"user","content":"hi"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "generic-compatible",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	for _, path := range []string{"reasoning", "reasoningSummary", "include", "verbosity"} {
		if value := gjson.GetBytes(gotBody, path); !value.Exists() {
			t.Fatalf("%s should remain for non-mistral provider; body=%s", path, string(gotBody))
		}
	}
}

func TestOpenAICompatExecutor_StripsUnsupportedDeepSeekLikeTopLevelFields(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"deepseek-v4-pro","reasoning":{"effort":"high"},"reasoningSummary":"auto","include":["reasoning.encrypted_content"],"reasoning_effort":"high","verbosity":"low","thinking":{"type":"enabled"},"interleaved":{"field":"reasoning_content"},"messages":[{"role":"user","content":"hi"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-pro",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	for _, path := range []string{"reasoning", "reasoningSummary", "include", "verbosity", "interleaved"} {
		if value := gjson.GetBytes(gotBody, path); value.Exists() {
			t.Fatalf("%s should be stripped for deepseek-like upstream; body=%s", path, string(gotBody))
		}
	}
	for _, path := range []string{"thinking", "reasoning_effort"} {
		if value := gjson.GetBytes(gotBody, path); !value.Exists() {
			t.Fatalf("%s should remain for deepseek-like upstream; body=%s", path, string(gotBody))
		}
	}
}

func TestOpenAICompatExecutor_StripsUnsupportedDeepSeekLikeTopLevelFieldsInStream(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"deepseek-v4-pro","stream":true,"reasoning":{"effort":"high"},"reasoningSummary":"auto","include":["reasoning.encrypted_content"],"reasoning_effort":"high","verbosity":"low","thinking":{"type":"enabled"},"interleaved":{"field":"reasoning_content"},"messages":[{"role":"user","content":"hi"}]}`)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-pro",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}

	for _, path := range []string{"reasoning", "reasoningSummary", "include", "verbosity", "interleaved"} {
		if value := gjson.GetBytes(gotBody, path); value.Exists() {
			t.Fatalf("%s should be stripped for deepseek-like stream upstream; body=%s", path, string(gotBody))
		}
	}
	for _, path := range []string{"thinking", "reasoning_effort", "stream_options.include_usage"} {
		if value := gjson.GetBytes(gotBody, path); !value.Exists() {
			t.Fatalf("%s should remain for deepseek-like stream upstream; body=%s", path, string(gotBody))
		}
	}
}

func TestOpenAICompatExecutor_NonNvidiaCompatLeavesMaxTokens(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
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
		Model:   "deepseek-ai/deepseek-v3.2",
		Payload: []byte(`{"model":"deepseek-ai/deepseek-v3.2","messages":[{"role":"user","content":"hi"}],"max_tokens":32000}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 32000 {
		t.Fatalf("max_tokens = %d, want %d", got, 32000)
	}
}

func TestOpenAICompatExecutor_NvidiaCompatReducesMaxTokensForStream(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "nvidia-nvapi",
			BaseURL: server.URL + "/v1",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "nvidia-nvapi",
	}}

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-ai/deepseek-v3.2",
		Payload: []byte(`{"model":"deepseek-ai/deepseek-v3.2","messages":[{"role":"user","content":"hi"}],"max_tokens":32000}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	drainStreamChunks(t, stream.Chunks)

	if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 31998 {
		t.Fatalf("max_tokens = %d, want %d", got, 31998)
	}
}

func TestOpenAICompatExecutor_NvidiaCompatLeavesSmallMaxTokens(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "nvidia-nvapi",
			BaseURL: server.URL + "/v1",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "nvidia-nvapi",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-ai/deepseek-v3.2",
		Payload: []byte(`{"model":"deepseek-ai/deepseek-v3.2","messages":[{"role":"user","content":"hi"}],"max_tokens":2}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 2 {
		t.Fatalf("max_tokens = %d, want %d", got, 2)
	}
}

func TestOpenAICompatExecutor_NvidiaCompatNormalizesToolCallIDs(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "nvidia-nvapi",
			BaseURL: server.URL + "/v1",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "nvidia-nvapi",
	}}

	payload := []byte(`{"model":"mistralai/mistral-medium-3.5-128b","messages":[{"role":"assistant","tool_calls":[{"id":"dowrite:1","type":"function","function":{"name":"aft_conflicts","arguments":"{}"}}]},{"role":"tool","tool_call_id":"dowrite:1","content":"ok"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mistralai/mistral-medium-3.5-128b",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	toolID := gjson.GetBytes(gotBody, "messages.0.tool_calls.0.id").String()
	toolResultID := gjson.GetBytes(gotBody, "messages.1.tool_call_id").String()
	if toolID == "dowrite:1" {
		t.Fatalf("tool call id should be normalized; body=%s", string(gotBody))
	}
	if toolID != toolResultID {
		t.Fatalf("tool id %q != tool result id %q; body=%s", toolID, toolResultID, string(gotBody))
	}
	if len(toolID) != 9 {
		t.Fatalf("normalized tool id length = %d, want 9; id=%q body=%s", len(toolID), toolID, string(gotBody))
	}
	for _, ch := range toolID {
		if !(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') && !(ch >= '0' && ch <= '9') {
			t.Fatalf("normalized tool id contains non-alnum %q in %q", ch, toolID)
		}
	}
}

func TestOpenAICompatExecutor_NonNvidiaCompatKeepsToolCallIDs(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
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

	payload := []byte(`{"model":"generic-compatible","messages":[{"role":"assistant","tool_calls":[{"id":"dowrite:1","type":"function","function":{"name":"aft_conflicts","arguments":"{}"}}]},{"role":"tool","tool_call_id":"dowrite:1","content":"ok"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "generic-compatible",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "messages.0.tool_calls.0.id").String(); got != "dowrite:1" {
		t.Fatalf("tool call id = %q, want dowrite:1; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "messages.1.tool_call_id").String(); got != "dowrite:1" {
		t.Fatalf("tool_call_id = %q, want dowrite:1; body=%s", got, string(gotBody))
	}
}

func TestOpenAICompatExecutor_MistralMedium35ModelNormalizesToolCallIDs(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
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

	payload := []byte(`{"model":"mistralai/mistral-medium-3.5-128b","messages":[{"role":"assistant","tool_calls":[{"id":"dowrite:1","type":"function","function":{"name":"aft_conflicts","arguments":"{}"}}]},{"role":"tool","tool_call_id":"dowrite:1","content":"ok"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mistralai/mistral-medium-3.5-128b",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	toolID := gjson.GetBytes(gotBody, "messages.0.tool_calls.0.id").String()
	toolResultID := gjson.GetBytes(gotBody, "messages.1.tool_call_id").String()
	if toolID == "dowrite:1" {
		t.Fatalf("mistral-medium-3.5 tool call id should be normalized; body=%s", string(gotBody))
	}
	if toolID != toolResultID {
		t.Fatalf("tool id %q != tool result id %q; body=%s", toolID, toolResultID, string(gotBody))
	}
	if len(toolID) != 9 {
		t.Fatalf("normalized mistral-medium-3.5 tool id length = %d, want 9; id=%q body=%s", len(toolID), toolID, string(gotBody))
	}
}

func TestOpenAICompatExecutor_NvidiaCompatNormalizesToolCallIDsForStream(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "nvidia-nvapi",
			BaseURL: server.URL + "/v1",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "nvidia-nvapi",
	}}

	payload := []byte(`{"model":"mistralai/mistral-medium-3.5-128b","messages":[{"role":"assistant","tool_calls":[{"id":"dowrite:1","type":"function","function":{"name":"aft_conflicts","arguments":"{}"}}]},{"role":"tool","tool_call_id":"dowrite:1","content":"ok"}]}`)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mistralai/mistral-medium-3.5-128b",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	drainStreamChunks(t, stream.Chunks)

	toolID := gjson.GetBytes(gotBody, "messages.0.tool_calls.0.id").String()
	toolResultID := gjson.GetBytes(gotBody, "messages.1.tool_call_id").String()
	if toolID == "dowrite:1" {
		t.Fatalf("tool call id should be normalized for stream; body=%s", string(gotBody))
	}
	if toolID != toolResultID {
		t.Fatalf("tool id %q != tool result id %q; body=%s", toolID, toolResultID, string(gotBody))
	}
	if len(toolID) != 9 {
		t.Fatalf("normalized stream tool id length = %d, want 9; id=%q body=%s", len(toolID), toolID, string(gotBody))
	}
}

func TestOpenAICompatExecutor_MistralMedium35ModelNormalizesToolCallIDsForStream(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
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

	payload := []byte(`{"model":"mistralai/mistral-medium-3.5-128b","messages":[{"role":"assistant","tool_calls":[{"id":"dowrite:1","type":"function","function":{"name":"aft_conflicts","arguments":"{}"}}]},{"role":"tool","tool_call_id":"dowrite:1","content":"ok"}]}`)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mistralai/mistral-medium-3.5-128b",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	drainStreamChunks(t, stream.Chunks)

	toolID := gjson.GetBytes(gotBody, "messages.0.tool_calls.0.id").String()
	toolResultID := gjson.GetBytes(gotBody, "messages.1.tool_call_id").String()
	if toolID == "dowrite:1" {
		t.Fatalf("mistral-medium-3.5 stream tool call id should be normalized; body=%s", string(gotBody))
	}
	if toolID != toolResultID {
		t.Fatalf("tool id %q != tool result id %q; body=%s", toolID, toolResultID, string(gotBody))
	}
	if len(toolID) != 9 {
		t.Fatalf("normalized mistral-medium-3.5 stream tool id length = %d, want 9; id=%q body=%s", len(toolID), toolID, string(gotBody))
	}
}

func drainStreamChunks(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk) {
	t.Helper()
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if payload := chunk.Payload; len(payload) > 0 {
			scanner := bufio.NewScanner(bytes.NewReader(payload))
			for scanner.Scan() {
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("scan stream payload: %v", err)
			}
		}
	}
}

func TestOpenAICompatExecutorStreamRejectsPlainJSONAfterBlankLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("\n\n: openrouter processing\n\nevent: error\n"))
		_, _ = w.Write([]byte(`{"error":{"message":"upstream failed","type":"server_error"}}` + "\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "openrouter-model",
		Payload: []byte(`{"model":"openrouter-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var gotErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
	}
	if gotErr == nil {
		t.Fatalf("expected plain JSON stream error")
	}
	if status, ok := gotErr.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream error status = %v, want %d", gotErr, http.StatusBadGateway)
	}
	if !strings.Contains(gotErr.Error(), "upstream failed") {
		t.Fatalf("stream error = %v", gotErr)
	}
}

func TestOpenAICompatExecutorStreamSkipsKeepAliveUntilDataLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("\n\n: openrouter processing\n\nevent: ping\nid: 1\nretry: 1000\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}` + "\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "openrouter-model",
		Payload: []byte(`{"model":"openrouter-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var got strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		got.Write(chunk.Payload)
	}
	if gjson.Get(got.String(), "choices.0.delta.content").String() != "hello" {
		t.Fatalf("stream payload = %s", got.String())
	}
}

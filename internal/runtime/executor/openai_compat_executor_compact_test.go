package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
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

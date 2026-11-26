package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type streamOnlyUpstream struct {
	stream func(w io.Writer)
}

func (s streamOnlyUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !ojson.NewDoc(body).Get("stream").Bool() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Streaming is required for this endpoint. Please enable streaming in your request (set 'stream: true').","type":"invalid_request_error","code":"streaming_required","param":"stream"}}`)
		return
	}
	var buf bytes.Buffer
	s.stream(&buf)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

func streamOnlyChatSSE(w io.Writer) {
	_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion\",\"created\":1,\"model\":\"upstream\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"po\"},\"finish_reason\":null}]}\n\n")
	_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion\",\"created\":1,\"model\":\"upstream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ng\"},\"finish_reason\":null}]}\n\n")
	_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion\",\"created\":1,\"model\":\"upstream\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func streamOnlyResponsesSSE(w io.Writer) {
	_, _ = io.WriteString(w, "event: response.created\n")
	_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":\"upstream\"}}\n\n")
	_, _ = io.WriteString(w, "event: response.output_text.delta\n")
	_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n")
	_, _ = io.WriteString(w, "event: response.completed\n")
	_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"model\":\"upstream\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"pong\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
}

func streamOnlyExecutor(baseURL string, streamOnly bool, endpoints []string) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
	model := config.OpenAICompatibilityModel{Name: "upstream", Alias: "alias"}
	if endpoints != nil {
		model.SupportedEndpoints = endpoints
	}
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "streamonly", BaseURL: baseURL, StreamOnly: streamOnly,
		Models: []config.OpenAICompatibilityModel{model},
	}}}
	return NewOpenAICompatExecutor("streamonly", cfg), &cliproxyauth.Auth{ID: "so-auth", Provider: "streamonly", Attributes: map[string]string{
		"base_url": baseURL, "api_key": "test-key", "compat_name": "streamonly",
	}}
}

func TestOpenAICompatStreamOnlyFoldsChatCompletions(t *testing.T) {
	server := httptest.NewServer(streamOnlyUpstream{stream: streamOnlyChatSSE})
	defer server.Close()

	e, auth := streamOnlyExecutor(server.URL, true, nil)
	req := cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	resp, err := e.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	payload := string(resp.Payload)
	if !strings.Contains(payload, "pong") {
		t.Fatalf("folded content missing: %s", payload)
	}
	if strings.Contains(payload, "data:") || strings.Contains(payload, `"delta"`) {
		t.Fatalf("client received a raw stream instead of one completion: %s", payload)
	}
	if !strings.Contains(payload, `"total_tokens":5`) {
		t.Fatalf("usage missing from folded body: %s", payload)
	}
	if got := resp.Headers.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("folded response must present JSON, got Content-Type %q", got)
	}
	if resp.Headers.Get("Content-Length") != "" {
		t.Fatalf("stale Content-Length leaked from the event stream: %q", resp.Headers.Get("Content-Length"))
	}
}

func TestOpenAICompatStreamOnlyFlagOffKeepsBufferedRequest(t *testing.T) {
	var sawStream bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sawStream = ojson.NewDoc(body).Get("stream").Bool()
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	e, auth := streamOnlyExecutor(server.URL, false, nil)
	req := cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	resp, err := e.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if sawStream {
		t.Fatalf("stream-only flag off must not force stream:true")
	}
	if !strings.Contains(string(resp.Payload), "pong") {
		t.Fatalf("unexpected payload: %s", resp.Payload)
	}
}

func TestOpenAICompatStreamOnlyFoldsResponses(t *testing.T) {
	server := httptest.NewServer(streamOnlyUpstream{stream: streamOnlyResponsesSSE})
	defer server.Close()

	e, auth := streamOnlyExecutor(server.URL, true, []string{"/responses"})
	req := cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"model":"alias","input":"hi"}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	resp, err := e.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	payload := string(resp.Payload)
	if !strings.Contains(payload, `"status":"completed"`) || !strings.Contains(payload, "pong") {
		t.Fatalf("folded response missing: %s", payload)
	}
	if strings.Contains(payload, "response.output_text.delta") {
		t.Fatalf("client received raw events instead of one response object: %s", payload)
	}
	if got := resp.Headers.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("folded response must present JSON, got Content-Type %q", got)
	}
}

func TestOpenAICompatStreamOnlySurfacesUpstreamRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"Free models require credit balance above $1, please topup credit","type":"invalid_request_error","code":"free_model_requires_credits","param":null}}`)
	}))
	defer server.Close()

	e, auth := streamOnlyExecutor(server.URL, true, nil)
	req := cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	_, err := e.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("Execute must fail on an upstream 402")
	}
	if !strings.Contains(err.Error(), "free_model_requires_credits") {
		t.Fatalf("upstream error not surfaced: %v", err)
	}
}

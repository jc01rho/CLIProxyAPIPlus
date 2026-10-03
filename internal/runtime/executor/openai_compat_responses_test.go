package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const compatNativeResponse = `{"id":"resp_native","object":"response","model":"upstream-version","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}`

func nativeResponsesExecutor(baseURL string) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "native", BaseURL: baseURL,
		Models: []config.OpenAICompatibilityModel{{Name: "upstream", Alias: "alias", SupportedEndpoints: []string{"/responses"}}},
	}}}
	return NewOpenAICompatExecutor("native", cfg), &cliproxyauth.Auth{ID: "native-auth", Provider: "native", Attributes: map[string]string{
		"base_url": baseURL, "api_key": "test-key", "compat_name": "native", "header:X-Custom": "custom-value",
	}}
}

func TestOpenAICompatNativeResponsesCapture(t *testing.T) {
	type capture struct {
		path    string
		body    []byte
		headers http.Header
	}
	requests := make(chan capture, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests <- capture{r.URL.Path, body, r.Header.Clone()}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, compatNativeResponse)
	}))
	defer server.Close()
	executor, auth := nativeResponsesExecutor(server.URL + "/v1")
	payload := []byte(`{"model":"alias","input":"hi","previous_response_id":"resp_previous","store":true,"reasoning":{"effort":"none"},"max_output_tokens":100,"tools":[{"type":"web_search_preview"},{"type":"custom","name":"native-tool","format":{"type":"text"}}],"metadata":{"arbitrary":"kept"}}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "upstream", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	got := <-requests
	if got.path != "/v1/responses" {
		t.Fatalf("upstream path = %q; body=%s", got.path, got.body)
	}
	for _, path := range []string{"input", "previous_response_id", "store", "reasoning", "max_output_tokens", "tools", "metadata"} {
		if gjson.GetBytes(got.body, path).Raw != gjson.GetBytes(payload, path).Raw {
			t.Errorf("%s changed: %s", path, got.body)
		}
	}
	if gjson.GetBytes(got.body, "model").String() != "upstream" || gjson.GetBytes(got.body, "messages").Exists() {
		t.Errorf("upstream body = %s", got.body)
	}
	if got.headers.Get("Authorization") != "Bearer test-key" || got.headers.Get("X-Custom") != "custom-value" {
		t.Errorf("headers = %v", got.headers)
	}
	if gjson.GetBytes(resp.Payload, "id").String() != "resp_native" {
		t.Errorf("response = %s", resp.Payload)
	}
}

func captureNativeResponsesUsage(t *testing.T) (context.Context, *multiProviderUsageCapture) {
	t.Helper()
	capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return coreusage.WithRequestedModelAlias(ctx, t.Name()), capture
}

func TestOpenAICompatNativeResponsesProtocols(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude} {
		for _, stream := range []bool{false, true} {
			name := format.String() + "/json"
			if stream {
				name = format.String() + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				ctx, capture := captureNativeResponsesUsage(t)
				requests := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					requests <- body
					if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
						t.Errorf("upstream = %s %s", r.Method, r.URL.Path)
					}
					if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("X-Custom") != "custom-value" {
						t.Errorf("headers = %v", r.Header)
					}
					if !stream {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, compatNativeResponse)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					// Split network writes, CRLF, event-only discriminators and multiline data.
					for _, part := range []string{
						": keepalive\r\nid: 1\r\nevent: response.created\r\nda",
						"ta: {\"response\":{\"id\":\"resp_native\",\"model\":\"upstream-version\",\"created_at\":123}}\r\n\r\n",
						"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\ndata: \"delta\":\"hello\",\"output_index\":0,\"content_index\":0}\n\n",
						"event: response.completed\ndata: {\"response\":" + compatNativeResponse + "}\n\n",
					} {
						_, _ = io.WriteString(w, part)
						w.(http.Flusher).Flush()
					}
				}))
				defer server.Close()
				executor, auth := nativeResponsesExecutor(server.URL + "/v1/")
				executor.cfg.OpenAICompatibility[0].Models[0].Image = true
				payload := []byte(`{"model":"alias","input":"hi","store":true,"previous_response_id":"prev","tools":[{"type":"web_search_preview"}]}`)
				if format != sdktranslator.FormatOpenAIResponse {
					payload = []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}],"max_tokens":90,"temperature":0.5}`)
				}
				opts := cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: payload, Stream: stream}
				req := cliproxyexecutor.Request{Model: "upstream", Payload: payload}
				var output []byte
				if stream {
					result, err := executor.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output = append(output, chunk.Payload...)
					}
					terminal := `"finish_reason":"stop"`
					if format == sdktranslator.FormatOpenAIResponse {
						terminal = "event: response.completed\ndata: "
					}
					if format == sdktranslator.FormatClaude {
						terminal = "event: message_stop"
					}
					if !strings.Contains(string(output), terminal) || !strings.Contains(string(output), "hello") {
						t.Errorf("stream = %s", output)
					}
				} else {
					response, err := executor.Execute(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output = response.Payload
					path := "output.0.content.0.text"
					if format == sdktranslator.FormatOpenAI {
						path = "choices.0.message.content"
					}
					if format == sdktranslator.FormatClaude {
						path = "content.0.text"
					}
					if gjson.GetBytes(output, path).String() != "hello" {
						t.Errorf("response = %s", output)
					}
				}
				body := <-requests
				if !gjson.GetBytes(body, "input").Exists() || gjson.GetBytes(body, "messages").Exists() || gjson.GetBytes(body, "stream_options.include_usage").Exists() {
					t.Errorf("upstream body = %s", body)
				}
				if gjson.GetBytes(body, "model").String() != "upstream" || gjson.GetBytes(body, "stream").Bool() != stream {
					t.Errorf("upstream routing = %s", body)
				}
				if format == sdktranslator.FormatOpenAIResponse {
					for _, path := range []string{"store", "previous_response_id", "tools"} {
						if gjson.GetBytes(body, path).Raw != gjson.GetBytes(payload, path).Raw {
							t.Errorf("native field %s changed: %s", path, body)
						}
					}
				} else if gjson.GetBytes(body, "max_output_tokens").Int() != 90 || gjson.GetBytes(body, "temperature").Float() != 0.5 {
					t.Errorf("generation parameters = %s", body)
				}
				record := capture.await(t)
				if record.Failed || record.ResponseModel != "upstream-version" || record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 7 || record.Detail.CachedTokens != 3 || record.Detail.ReasoningTokens != 2 {
					t.Errorf("usage = %+v", record)
				}
			})
		}
	}
}

func TestOpenAICompatNativeResponsesFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		status         int
		stream, failed bool
		tokens         int64
	}{
		{"http", `{"error":{"message":"bad request"}}`, 400, false, true, 0},
		{"http-stream", `{"error":{"message":"bad request"}}`, 429, true, true, 0},
		{"json-error", `{"error":{"message":"bad request"}}`, 200, false, true, 0},
		{"json-failed", `{"id":"r","status":"failed","model":"upstream-version","usage":{"input_tokens":3}}`, 200, false, true, 3},
		{"stream-json-error", `{"error":{"message":"bad request"}}`, 200, true, true, 0},
		{"failed-before-tokens", "event: response.failed\ndata: {\"response\":{\"status\":\"failed\",\"usage\":{\"input_tokens\":3},\"error\":{\"message\":\"failed\"}}}\n\n", 200, true, true, 3},
		{"sse-error", "event: error\ndata: {\"message\":\"failed\"}\n\n", 200, true, true, 0},
		{"eof", "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{}}\n\n", 200, true, true, 0},
		{"partial-terminal", "data: {\"type\":\"response.completed\",\"response\":{}}\n", 200, true, true, 0},
		{"done-without-terminal", "data: [DONE]\n\n", 200, true, true, 0},
		{"malformed-event", "data: {\n\n", 200, true, true, 0},
		{"incomplete", "event: response.incomplete\ndata: {\"response\":{\"id\":\"r\",\"status\":\"incomplete\",\"usage\":{\"input_tokens\":3},\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", 200, true, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, capture := captureNativeResponsesUsage(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.stream && tc.name != "stream-json-error" {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			executor, auth := nativeResponsesExecutor(server.URL)
			req := cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"input":"hi"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			var err error
			var output []byte
			if tc.stream {
				var result *cliproxyexecutor.StreamResult
				result, err = executor.ExecuteStream(ctx, auth, req, opts)
				if err == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
						output = append(output, chunk.Payload...)
					}
				}
			} else {
				_, err = executor.Execute(ctx, auth, req, opts)
			}
			if (err != nil) != tc.failed {
				t.Fatalf("error = %v, output=%s", err, output)
			}
			if tc.name == "failed-before-tokens" && len(output) != 0 {
				t.Errorf("failed event emitted as success: %s", output)
			}
			if tc.name == "eof" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("EOF error = %v", err)
			}
			if !tc.failed && !strings.Contains(string(output), "event: response.incomplete") {
				t.Errorf("missing incomplete terminal: %s", output)
			}
			record := capture.await(t)
			if record.Failed != tc.failed || record.Detail.InputTokens != tc.tokens {
				t.Errorf("usage = %+v", record)
			}
		})
	}
}

func TestOpenAICompatNativeResponsesCancellation(t *testing.T) {
	ctx, capture := captureNativeResponsesUsage(t)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	upstreamCancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamCancelled)
	}))
	defer server.Close()
	executor, auth := nativeResponsesExecutor(server.URL)
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"input":"hi"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case first := <-result.Chunks:
		if first.Err != nil || len(first.Payload) == 0 {
			t.Fatalf("first chunk=%+v", first)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	select {
	case <-upstreamCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream not cancelled")
	}
	for range result.Chunks {
	}
	if record := capture.await(t); !record.Failed {
		t.Errorf("cancellation usage=%+v", record)
	}
}

func TestOpenAICompatNativeResponsesRouteSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		endpoints []string
		zen       bool
		want      string
	}{
		{"responses", []string{"responses", " /responses "}, false, "/responses"},
		{"chat", []string{"/chat/completions"}, false, "/chat/completions"},
		{"both", []string{"/responses", "chat/completions"}, false, "/chat/completions"},
		{"unspecified", nil, false, "/chat/completions"},
		{"zen-default", nil, true, "/responses"},
		{"zen-chat", []string{"/chat/completions"}, true, "/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				_, _ = io.WriteString(w, compatNativeResponse)
			}))
			defer server.Close()
			executor, auth := nativeResponsesExecutor(server.URL)
			compat := &executor.cfg.OpenAICompatibility[0]
			compat.Models[0].SupportedEndpoints = tc.endpoints
			// A conflicting alias precedes the resolved upstream model.
			compat.Models = append([]config.OpenAICompatibilityModel{{Name: "other", Alias: "upstream", SupportedEndpoints: []string{"/responses"}}}, compat.Models...)
			if tc.zen {
				compat.BaseURL = "https://opencode.ai/zen/v1"
			}
			// A different provider exposes the same alias and must not affect routing.
			executor.cfg.OpenAICompatibility = append([]config.OpenAICompatibility{{Name: "wrong-provider", Models: []config.OpenAICompatibilityModel{{Name: "upstream", Alias: "alias", SupportedEndpoints: []string{"/responses"}}}}}, executor.cfg.OpenAICompatibility...)
			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"model":"alias","input":"hi"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if err != nil {
				t.Fatal(err)
			}
			if path := <-paths; path != tc.want {
				t.Errorf("path=%s, want %s", path, tc.want)
			}
		})
	}
}

func TestOpenAICompatNativeResponsesPayloadAndThinking(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		_, _ = io.WriteString(w, compatNativeResponse)
	}))
	defer server.Close()
	executor, auth := nativeResponsesExecutor(server.URL)
	executor.cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "upstream", Protocol: "openai-response"}}, Params: map[string]any{"reasoning.effort": "low", "metadata.configured": true}}}
	payload := []byte(`{"input":"hi","reasoning":{"effort":"medium"}}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "upstream(high)", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	body := <-requests
	if gjson.GetBytes(body, "reasoning.effort").String() != "low" || !gjson.GetBytes(body, "metadata.configured").Bool() {
		t.Errorf("body=%s", body)
	}
	// The suffix applies when no payload override wins.
	executor.cfg.Payload.Override = nil
	_, err = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "upstream(high)", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	if body = <-requests; gjson.GetBytes(body, "reasoning.effort").String() != "high" {
		t.Errorf("suffix body=%s", body)
	}
}

func TestOpenAICompatNativeResponsesIncompleteTranslation(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatClaude} {
		for _, stream := range []bool{false, true} {
			t.Run(format.String()+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				body, _ := sjson.Set(compatNativeResponse, "status", "incomplete")
				body, _ = sjson.Set(body, "incomplete_details.reason", "max_output_tokens")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response.incomplete\",\"response\":"+body+"}\n\n")
					} else {
						_, _ = io.WriteString(w, body)
					}
				}))
				defer server.Close()
				executor, auth := nativeResponsesExecutor(server.URL)
				req := cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: format}
				var output []byte
				if stream {
					result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output = append(output, chunk.Payload...)
					}
				} else {
					result, err := executor.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output = result.Payload
				}
				want := `"finish_reason":"length"`
				if format == sdktranslator.FormatClaude {
					want = `"stop_reason":"max_tokens"`
				}
				if !strings.Contains(string(output), want) {
					t.Errorf("incomplete output=%s", output)
				}
			})
		}
	}
}

func TestOpenAICompatNativeResponsesProxyAndOAuthScope(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			requests := make(chan []byte, 1)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.String() != "http://native.invalid/v1/responses" {
					t.Errorf("proxy URL=%s", r.URL)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				requests <- body
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":"+compatNativeResponse+"}\n\n")
				} else {
					_, _ = io.WriteString(w, compatNativeResponse)
				}
			}))
			defer proxy.Close()
			executor, auth := nativeResponsesExecutor("http://native.invalid/v1")
			auth.ProxyURL = proxy.URL
			// orphan-delegation-compatibility now lives under upstream.codex and
			// applies to API-key credentials too, so only the client-side Codex
			// multi-agent rewrite is checked here.
			cfg, err := config.ParseConfigBytes([]byte("oauth:\n  providers:\n    codex: {optimize-multi-agent-v2: true}\n"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.OpenAICompatibility = executor.cfg.OpenAICompatibility
			executor.cfg = cfg
			payload := []byte(`{"input":[{"type":"function_call_output","name":"create_thread","namespace":"codex_app","output":"<codex_delegation>task</codex_delegation>"}],"store":true,"previous_response_id":"prev","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}]}`)
			req := cliproxyexecutor.Request{Model: "upstream", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: http.Header{"User-Agent": {"codex_cli_rs/0.144.1"}, "X-Openai-Subagent": {"collab_spawn"}}}
			if stream {
				result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				drainStreamChunks(t, result.Chunks)
			} else {
				if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			body := <-requests
			for _, field := range []string{"input", "tools", "store", "previous_response_id"} {
				if gjson.GetBytes(body, field).Raw != gjson.GetBytes(payload, field).Raw {
					t.Errorf("OAuth settings changed %s: %s", field, body)
				}
			}
			if !cfg.Client.Codex.OptimizeMultiAgentV2 {
				t.Error("shared OAuth config changed")
			}
		})
	}
}

func TestOpenAICompatNativeResponsesAliasAndCompact(t *testing.T) {
	for _, alt := range []string{"", "responses/compact"} {
		t.Run("alt="+alt, func(t *testing.T) {
			paths := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				_, _ = io.WriteString(w, compatNativeResponse)
			}))
			defer server.Close()
			executor, auth := nativeResponsesExecutor(server.URL)
			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "alias", Payload: []byte(`{"input":"hi"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: alt})
			if err != nil {
				t.Fatal(err)
			}
			want := "/responses"
			if alt != "" {
				want = "/responses/compact"
			}
			if path := <-paths; path != want {
				t.Errorf("path=%s, want %s", path, want)
			}
		})
	}
}

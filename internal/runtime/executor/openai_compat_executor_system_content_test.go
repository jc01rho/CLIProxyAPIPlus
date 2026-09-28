package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorSystemContentAsString(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			for _, format := range []string{"openai", "claude"} {
				t.Run(fmt.Sprintf("stream=%t/enabled=%t/format=%s", stream, enabled, format), func(t *testing.T) {
					bodies := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						bodies <- body
						response := `{"id":"test","model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
						w.Header().Set("Content-Type", "application/json")
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							response = "data: {\"id\":\"test\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
						}
						if _, err = io.WriteString(w, response); err != nil {
							t.Error(err)
						}
					}))
					defer server.Close()
					cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
						Name: "test-compat", BaseURL: server.URL, SystemContentAsString: enabled,
					}}}
					executor := NewOpenAICompatExecutor("test-compat", cfg)
					auth := &cliproxyauth.Auth{Provider: "test-compat", Attributes: map[string]string{
						"base_url": server.URL, "compat_name": "test-compat", "api_key": "test-key",
					}}
					payload := `{"model":"test-model","messages":[{"role":"system","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]},{"role":"user","content":"hello"}]}`
					if format == "claude" {
						payload = `{"model":"test-model","max_tokens":64,"system":[{"type":"text","text":"first"},{"type":"text","text":"second"}],"messages":[{"role":"user","content":"hello"}]}`
					}
					req := cliproxyexecutor.Request{Model: "test-model", Payload: []byte(payload)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(format), Stream: stream}
					if stream {
						result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
						t.Fatal(err)
					}
					select {
					case body := <-bodies:
						content := gjson.GetBytes(body, "messages.0.content")
						if enabled {
							if content.Type != gjson.String || content.String() != "first\nsecond" {
								t.Fatalf("system content = %s, want joined string", content.Raw)
							}
						} else if !content.IsArray() {
							t.Fatalf("disabled option changed system content: %s", content.Raw)
						}
					default:
						t.Fatal("upstream request missing")
					}
				})
			}
		}
	}
}

func TestOpenAICompatSystemContentAfterDeveloperFallback(t *testing.T) {
	for _, mode := range []string{"nonstream", "stream", "embedded-stream-error"} {
		t.Run(mode, func(t *testing.T) {
			stream := mode != "nonstream"
			bodies := make(chan []byte, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				bodies <- body
				response := `{"id":"test","model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
				w.Header().Set("Content-Type", "application/json")
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					response = "data: {\"id\":\"test\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
				}
				if gjson.GetBytes(body, "messages.0.role").String() == "developer" {
					response = `{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`
					if mode == "embedded-stream-error" {
						response = "data: " + response + "\n\n"
					} else {
						w.WriteHeader(http.StatusBadRequest)
					}
				}
				if _, err = io.WriteString(w, response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "test-compat", BaseURL: server.URL, SystemContentAsString: true,
			}}}
			executor := NewOpenAICompatExecutor("test-compat", cfg)
			auth := &cliproxyauth.Auth{Provider: "test-compat", Attributes: map[string]string{
				"base_url": server.URL, "compat_name": "test-compat", "api_key": "test-key",
			}}
			req := cliproxyexecutor.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","messages":[{"role":"developer","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]},{"role":"user","content":"hello"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: stream}
			// The second execution uses the capability learned from the first retry.
			for attempt := 0; attempt < 2; attempt++ {
				if stream {
					result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			if len(bodies) != 3 {
				t.Fatalf("requests = %d, want initial request, retry and cached fallback", len(bodies))
			}
			original := <-bodies
			if !gjson.GetBytes(original, "messages.0.content").IsArray() {
				t.Fatal("developer array changed before role fallback")
			}
			for _, step := range []string{"retry", "cached fallback"} {
				body := <-bodies
				if role := gjson.GetBytes(body, "messages.0.role").String(); role != "system" {
					t.Fatalf("%s role = %q, want system", step, role)
				}
				content := gjson.GetBytes(body, "messages.0.content")
				if content.Type != gjson.String || content.String() != "first\nsecond" {
					t.Errorf("%s system content = %s, want joined string", step, content.Raw)
				}
			}
		})
	}
}

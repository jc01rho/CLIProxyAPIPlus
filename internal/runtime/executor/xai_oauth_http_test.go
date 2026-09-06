package executor

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const xaiOAuthCompletedSSE = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_oauth\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"grok-4.3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"

func TestXAIOAuthHTTPContract(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "buffered"
		if stream {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, key, affinity               string
				present, overrides, configuredKey bool
			}{
				{name: "trimmed key", key: "  abc\t", affinity: "ba7816bf8f01cfea414140de5dae2223", present: true},
				{name: "second vector", key: "hello", affinity: "2cf24dba5fb0a30e26e83b2ac5b9e29e", present: true},
				{name: "missing key"},
				{name: "blank key", key: " \t ", present: true},
				{name: "mixed case overrides", key: "abc", present: true, overrides: true},
				{name: "effective configured key", key: "  abc\t", affinity: "ba7816bf8f01cfea414140de5dae2223", present: true, configuredKey: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					auth := &cliproxyauth.Auth{ID: "oauth-contract", Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "synthetic-token"}}
					want := map[string]string{
						"Authorization": "Bearer synthetic-token", "Content-Type": "application/json",
						"User-Agent": "opencodex-grok/0.2.93", "x-grok-client-identifier": "opencodex", "x-grok-client-version": "0.2.93",
						"x-xai-token-auth": "xai-grok-cli", "x-authenticateresponse": "authenticate-response",
						"x-grok-conv-id": tc.affinity, "x-grok-session-id": tc.affinity, "Accept-Encoding": "identity",
					}
					if tc.overrides {
						for header := range want {
							want[header] = "override-" + strings.ToLower(header)
							auth.Attributes["header:"+strings.ToUpper(header)] = want[header]
						}
						auth.Attributes["header:X-gRoK-rEq-Id"] = "configured-request"
						want["x-grok-req-id"] = "configured-request"
					}
					payload := []byte(`{"model":"grok-4.3","input":[{"role":"user","content":"hello"}]}`)
					if tc.present {
						// Quote through JSON so the body retains tabs and surrounding spaces.
						payload = []byte(`{"model":"grok-4.3","input":[],"prompt_cache_key":` + strconv.Quote(tc.key) + `}`)
					}
					calls := 0
					rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						if r.Method != http.MethodPost || r.URL.String() != xaiauth.CLIChatProxyBaseURL+"/responses" {
							t.Errorf("request = %s %s", r.Method, r.URL)
						}
						for header, value := range want {
							if got := r.Header.Get(header); got != value {
								t.Errorf("%s = %q, want %q", header, got, value)
							}
							count := 0
							for key, values := range r.Header {
								if strings.EqualFold(key, header) {
									count += len(values)
								}
							}
							if count > 1 {
								t.Errorf("duplicate %s: %v", header, r.Header)
							}
						}
						if !tc.overrides {
							if _, err := uuid.Parse(r.Header.Get("x-grok-req-id")); err != nil {
								t.Errorf("x-grok-req-id is not a UUID: %v", err)
							}
						}
						for _, header := range []string{"x-grok-agent-id", "x-grok-deployment-id", "x-grok-turn-id", "x-grok-user-id", "session_id", "x-client-request-id"} {
							if r.Header.Get(header) != "" {
								t.Errorf("unexpected identity header %s", header)
							}
						}
						body, err := io.ReadAll(r.Body)
						if err != nil {
							return nil, err
						}
						key := gjson.GetBytes(body, "prompt_cache_key")
						if key.Exists() != tc.present || key.String() != tc.key {
							t.Errorf("body prompt_cache_key = %s, want presence=%v value=%q", key.Raw, tc.present, tc.key)
						}
						if !gjson.GetBytes(body, "stream").Bool() {
							t.Error("upstream stream must stay true")
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(xaiOAuthCompletedSSE))}, nil
					})
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", rt)
					ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					cfg := &config.Config{}
					if tc.configuredKey {
						payload = []byte(`{"model":"grok-4.3","input":[],"prompt_cache_key":"original-key"}`)
						cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "grok-4.3"}}, Params: map[string]any{"prompt_cache_key": tc.key}}}
					}
					exec := NewXAIExecutor(cfg)
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "host-session-must-not-replace-key"}}
					req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: payload}
					if stream {
						result, err := exec.ExecuteStream(ctx, auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						if err := drainXAIOAuthStream(ctx, result); err != nil {
							t.Fatal(err)
						}
					} else if _, err := exec.Execute(ctx, auth, req, opts); err != nil {
						t.Fatal(err)
					}
					if calls != 1 {
						t.Errorf("HTTP calls = %d, want 1", calls)
					}
				})
			}
		})
	}
}

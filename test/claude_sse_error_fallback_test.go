package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	claudehandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestClaudeSSEErrorHTTPRouting(t *testing.T) {
	const model = "claude-opus-5"
	const fallback = "claude-sonnet-5"
	const prefix = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
	const failure = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"upstream_rate_limit\",\"http_status\":429,\"message\":\"capacity exhausted\",\"retryable\":true,\"retry_after\":8}}\n\n"
	const success = prefix + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, scenario := range []struct {
		name        string
		credentials int
		wantCalls   int32
		wantStatus  int
		wantErrors  int
	}{
		{"retry", 1, 2, http.StatusOK, 0},
		{"model fallback", 2, 3, http.StatusOK, 0},
		{"late error", 2, 1, http.StatusOK, 1},
		{"exhausted", 1, 1, http.StatusTooManyRequests, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// Given: real Claude SSE responses, including an HTTP 200 capacity error.
			var calls, fallbackCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				attempt := calls.Add(1)
				wire := failure
				if request.Model == fallback {
					fallbackCalls.Add(1)
					wire = success
				} else if scenario.name == "retry" {
					wire = strings.Replace(failure, `,"retry_after":8`, "", 1)
					if attempt > 1 {
						wire = success
					}
				} else if scenario.name == "late error" {
					wire = prefix + failure
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := io.WriteString(w, wire); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(upstream.Close)
			cfg := &config.Config{}
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(1, 0, 1)
			manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
			if scenario.name == "model fallback" || scenario.name == "late error" {
				manager.SetFallbackChain([]string{fallback}, 1)
			}
			for index := 0; index < scenario.credentials; index++ {
				id := fmt.Sprintf("%s-%d", t.Name(), index)
				registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}, {ID: fallback}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{
					ID: id, Provider: "claude", Status: cliproxyauth.StatusActive,
					Attributes: map[string]string{"api_key": id, "base_url": upstream.URL},
					Metadata:   map[string]any{"disable_cooling": scenario.name == "retry"},
				}); err != nil {
					t.Fatal(err)
				}
			}
			router := gin.New()
			handler := claudehandlers.NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router.POST("/v1/messages", handler.ClaudeMessages)
			proxy := httptest.NewServer(router)
			t.Cleanup(proxy.Close)

			// When: send a real downstream HTTP request through the production handler.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/v1/messages", strings.NewReader(`{"model":"claude-opus-5","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := proxy.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}

			// Then: retry/fallback before output, but never restart committed output.
			if response.StatusCode != scenario.wantStatus || calls.Load() != scenario.wantCalls {
				t.Fatalf("status=%d calls=%d, want status=%d calls=%d; body=%s", response.StatusCode, calls.Load(), scenario.wantStatus, scenario.wantCalls, body)
			}
			if got := strings.Count(string(body), "event: error"); got != scenario.wantErrors {
				t.Fatalf("error events=%d, want %d; body=%s", got, scenario.wantErrors, body)
			}
			if scenario.name == "model fallback" && fallbackCalls.Load() != 1 {
				t.Fatalf("fallback calls=%d, want 1", fallbackCalls.Load())
			}
			if scenario.wantStatus == http.StatusOK && strings.Count(string(body), `"text":"hello"`) != 1 {
				t.Fatalf("expected exactly one text output: %s", body)
			}
		})
	}
}

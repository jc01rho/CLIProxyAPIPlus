package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

type xaiFirstDeltaTransport struct{ http.RoundTripper }

func (p xaiFirstDeltaTransport) RoundTripperFor(*cliproxyauth.Auth) http.RoundTripper {
	return p.RoundTripper
}

func TestXAIOAuthHTTPFirstDeltaBeforeTerminal(t *testing.T) {
	for _, namedEvents := range []bool{false, true} {
		t.Run(map[bool]string{false: "data-only", true: "event-and-data"}[namedEvents], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			releaseTerminal := make(chan struct{})
			terminalSent := make(chan struct{})
			upstreamDone := make(chan error, 1)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseTerminal) }) }
			reader, writer := io.Pipe()
			t.Cleanup(func() { cancel(); release(); _ = reader.Close() })
			rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				go func() {
					defer func() { _ = writer.Close() }()
					writeEvent := func(payload string) error {
						frame := "data: " + payload + "\n\n"
						if namedEvents {
							frame = "event: " + gjson.Get(payload, "type").String() + "\n" + frame
						}
						_, err := io.WriteString(writer, frame)
						return err
					}
					for _, payload := range []string{
						`{"type":"response.created","response":{"id":"first-delta","object":"response","model":"grok-4.3","output":[]}}`,
						`{"type":"response.output_text.delta","item_id":"message","output_index":0,"content_index":0,"delta":"first"}`,
					} {
						if err := writeEvent(payload); err != nil {
							upstreamDone <- err
							return
						}
					}
					// Completion cannot unblock buffering: only the downstream client's
					// exact first-delta frame opens this gate, or cancellation ends it.
					select {
					case <-releaseTerminal:
					case <-r.Context().Done():
						upstreamDone <- r.Context().Err()
						return
					}
					close(terminalSent)
					upstreamDone <- writeEvent(`{"type":"response.completed","response":{"id":"first-delta","object":"response","model":"grok-4.3","status":"completed","output":[{"type":"message","id":"message","role":"assistant","content":[{"type":"output_text","text":"first"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`)
				}()
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
			})
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewXAIExecutor(&config.Config{CommercialMode: true}))
			manager.SetRoundTripperProvider(xaiFirstDeltaTransport{rt})
			auth := &cliproxyauth.Auth{ID: "first-delta-" + uuid.NewString(), Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "synthetic-token"}}
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "xai", []*registry.ModelInfo{{ID: "grok-4.3"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			handler := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
			router := gin.New()
			router.POST("/v1/responses", handler.Responses)
			server := httptest.NewServer(router)
			t.Cleanup(func() { cancel(); release(); server.CloseClientConnections(); server.Close() })
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"grok-4.3","input":"hi","stream":true,"prompt_cache_key":"abc"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatalf("first-delta request failed while terminal was gated: %v", err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("HTTP status=%d", response.StatusCode)
			}
			scanner := bufio.NewScanner(response.Body)
			var eventName, data string
			deltas, completed := 0, 0
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "event:") {
					eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
					continue
				}
				if strings.HasPrefix(line, "data:") {
					data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					continue
				}
				if line != "" || data == "" {
					continue
				}
				event := gjson.Parse(data)
				if eventName != "" && eventName != event.Get("type").String() {
					t.Errorf("event/data mismatch: %q, %s", eventName, data)
				}
				switch event.Get("type").String() {
				case "response.output_text.delta":
					deltas++
					if event.Get("delta").String() != "first" {
						t.Errorf("delta=%s", data)
					}
					select {
					case <-terminalSent:
						t.Error("terminal was sent before first delta")
					default:
					}
					release()
				case "response.completed":
					completed++
					if event.Get("response.usage.total_tokens").Int() != 3 {
						t.Errorf("usage=%s", data)
					}
				}
				eventName, data = "", ""
			}
			if deltas != 1 {
				t.Fatalf("first delta did not arrive before gated terminal: deltas=%d, read error=%v", deltas, scanner.Err())
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if completed != 1 {
				t.Errorf("completed events=%d, want 1", completed)
			}
			select {
			case err := <-upstreamDone:
				if err != nil {
					t.Fatal(fmt.Errorf("upstream fixture: %w", err))
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

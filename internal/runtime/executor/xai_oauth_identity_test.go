package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func drainXAIOAuthStream(ctx context.Context, result *cliproxyexecutor.StreamResult) error {
	for {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				return nil
			}
			if chunk.Err != nil {
				return chunk.Err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func TestXAIOAuthHTTPRequestIdentity(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "streaming"}[stream], func(t *testing.T) {
			var ids []string
			rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				ids = append(ids, r.Header.Get("x-grok-req-id"))
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(xaiOAuthCompletedSSE))}, nil
			})
			base := logging.WithRequestID(context.WithValue(context.Background(), "cliproxy.roundtripper", rt), "1234abcd")
			base, cancel := context.WithTimeout(base, 10*time.Second)
			defer cancel()
			ctx := cliproxyexecutor.WithXAIRequestIdentity(base)
			auth := &cliproxyauth.Auth{ID: "credential-one", Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "synthetic-old"}}
			exec := NewXAIExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"model":"grok-4.3","input":[],"prompt_cache_key":"abc"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			send := func(ctx context.Context, auth *cliproxyauth.Auth) {
				t.Helper()
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
			}
			send(ctx, auth)
			send(context.WithValue(ctx, "retry-attempt", 2), auth.Clone())
			refreshed := auth.Clone()
			refreshed.Metadata["access_token"] = "synthetic-refreshed"
			send(ctx, refreshed)
			rotated := refreshed.Clone()
			rotated.ID = "credential-two"
			send(ctx, rotated)
			send(ctx, auth)
			send(cliproxyexecutor.WithXAIRequestIdentity(base), auth)
			send(base, auth)
			send(base, auth)
			if len(ids) != 8 {
				t.Fatalf("requests = %d", len(ids))
			}
			for _, id := range ids {
				if parsed, err := uuid.Parse(id); err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
					t.Errorf("request ID %q must be RFC4122 UUIDv4: %v", id, err)
				}
			}
			for _, index := range []int{1, 2, 4} {
				if ids[index] != ids[0] {
					t.Errorf("same-target replay %d changed ID: %v", index, ids)
				}
			}
			for _, index := range []int{3, 5, 6, 7} {
				if ids[index] == ids[0] {
					t.Errorf("different target/request %d reused ID: %v", index, ids)
				}
			}
			if ids[6] == ids[7] {
				t.Errorf("unscoped executions reused ID: %v", ids)
			}
		})
	}
}

// Only token acquisition is replaced: the manager's 401 handling, credential
// update, attempt contexts and real HTTP executor all participate in the replay.
type xaiOAuthRefreshExecutor struct {
	*XAIExecutor
	refreshes int
}

func (e *xaiOAuthRefreshExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	e.refreshes++
	updated := auth.Clone()
	updated.Metadata["access_token"] = "synthetic-refreshed"
	return updated, nil
}

func TestXAIOAuthHTTPManagerRefreshIdentity(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "streaming"}[stream], func(t *testing.T) {
			var ids, tokens []string
			rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				ids = append(ids, r.Header.Get("x-grok-req-id"))
				tokens = append(tokens, r.Header.Get("Authorization"))
				status, body := http.StatusOK, xaiOAuthCompletedSSE
				if len(ids) == 1 {
					status, body = http.StatusUnauthorized, `{"error":{"message":"expired token"}}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			ctx := logging.WithRequestID(context.WithValue(context.Background(), "cliproxy.roundtripper", rt), "1234abcd")
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			exec := &xaiOAuthRefreshExecutor{XAIExecutor: NewXAIExecutor(&config.Config{})}
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(exec)
			auth := &cliproxyauth.Auth{ID: "oauth-refresh-" + uuid.NewString(), Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "synthetic-old", "refresh_token": "synthetic-refresh"}}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "xai", []*registry.ModelInfo{{ID: "grok-4.3"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatal(err)
			}
			req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"model":"grok-4.3","input":[],"prompt_cache_key":"abc"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream, Metadata: map[string]any{}}
			base := ctx
			for invocation := range 3 {
				if invocation == 0 || invocation == 2 {
					// HTTP bootstrap re-entry shares a Gin request, not caller metadata.
					ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					ctx = context.WithValue(base, "gin", ginCtx)
				}
				// Invocation 1 models the handler's same-request bootstrap replay.
				if stream {
					result, err := manager.ExecuteStream(ctx, []string{"xai"}, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					if err := drainXAIOAuthStream(ctx, result); err != nil {
						t.Fatal(err)
					}
				} else if _, err := manager.Execute(ctx, []string{"xai"}, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			if len(opts.Metadata) != 0 {
				t.Errorf("manager mutated caller metadata: %v", opts.Metadata)
			}
			if exec.refreshes != 1 || len(ids) != 4 {
				t.Fatalf("refreshes=%d requests=%d, want 1 and 4", exec.refreshes, len(ids))
			}
			if ids[0] == "" || ids[0] != ids[1] || ids[1] != ids[2] || ids[2] == ids[3] {
				t.Errorf("request identity lifecycle = %v; want initial == refresh == bootstrap != new request", ids)
			}
			if tokens[0] != "Bearer synthetic-old" || tokens[1] != "Bearer synthetic-refreshed" || tokens[2] != tokens[1] {
				t.Errorf("token replay lifecycle = %v", tokens)
			}
		})
	}
}

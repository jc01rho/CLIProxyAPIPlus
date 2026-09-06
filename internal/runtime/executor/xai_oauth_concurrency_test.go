package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestXAIOAuthHTTPConcurrentOptionsReuse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, owner := range []string{"independent SDK requests", "explicit SDK replay", "same HTTP request", "distinct websocket turns"} {
			t.Run(map[bool]string{false: "buffered/", true: "streaming/"}[stream]+owner, func(t *testing.T) {
				const workers = 12
				ids := make(chan string, workers+1)
				rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
					ids <- r.Header.Get("x-grok-req-id")
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(xaiOAuthCompletedSSE))}, nil
				})
				ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), "cliproxy.roundtripper", rt), 10*time.Second)
				defer cancel()
				sameRequest := owner == "explicit SDK replay" || owner == "same HTTP request"
				if owner == "explicit SDK replay" {
					ctx = cliproxyexecutor.WithXAIRequestIdentity(ctx)
				}
				if owner == "same HTTP request" || owner == "distinct websocket turns" {
					ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					ctx = context.WithValue(ctx, "gin", ginCtx)
					if owner == "distinct websocket turns" {
						ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
					}
				}
				manager := cliproxyauth.NewManager(nil, nil, nil)
				manager.SetRetryConfig(0, 0, 0)
				// This fixture concurrently replays one Gin request to test identity,
				// not the serial per-request deferred log capture. Commercial mode
				// disables that capture while retaining locked latest-route metadata.
				manager.RegisterExecutor(NewXAIExecutor(&config.Config{CommercialMode: true}))
				auth := &cliproxyauth.Auth{ID: "oauth-concurrent-" + uuid.NewString(), Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "synthetic-token"}}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "xai", []*registry.ModelInfo{{ID: "grok-4.3"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				if _, err := manager.Register(ctx, auth); err != nil {
					t.Fatal(err)
				}
				metadata := map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "shared-session", "caller-marker": "unchanged"}
				wantMetadata := map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "shared-session", "caller-marker": "unchanged"}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream, Metadata: metadata}
				req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"model":"grok-4.3","input":[],"prompt_cache_key":"abc"}`)}
				execute := func() error {
					if stream {
						result, err := manager.ExecuteStream(ctx, []string{"xai"}, req, opts)
						if err != nil {
							return err
						}
						return drainXAIOAuthStream(ctx, result)
					}
					_, err := manager.Execute(ctx, []string{"xai"}, req, opts)
					return err
				}
				// The first call proves immutability without relying on race scheduling.
				if err := execute(); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(metadata, wantMetadata) {
					t.Fatalf("caller metadata mutated: %v", metadata)
				}
				start := make(chan struct{})
				done := make(chan error, workers)
				for range workers {
					go func() { <-start; done <- execute() }()
				}
				close(start)
				for range workers {
					select {
					case err := <-done:
						if err != nil {
							t.Error(err)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				if !reflect.DeepEqual(metadata, wantMetadata) {
					t.Errorf("concurrent calls mutated metadata: %v", metadata)
				}
				if len(ids) != workers+1 {
					t.Fatalf("requests=%d, want %d", len(ids), workers+1)
				}
				unique := make(map[string]bool)
				for range workers + 1 {
					id := <-ids
					parsed, err := uuid.Parse(id)
					if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
						t.Errorf("request ID %q is not UUIDv4", id)
					}
					unique[id] = true
				}
				wantIDs := workers + 1
				if sameRequest {
					wantIDs = 1
				}
				if len(unique) != wantIDs {
					t.Errorf("unique request IDs=%d, want %d", len(unique), wantIDs)
				}
			})
		}
	}
}

func TestXAIOAuthIdentityDoesNotMutateOtherProviderMetadata(t *testing.T) {
	manager := cliproxyauth.NewManager(nil, nil, nil)
	metadata := map[string]any{"caller-marker": "unchanged"}
	opts := cliproxyexecutor.Options{Metadata: metadata}
	for _, stream := range []bool{false, true} {
		if stream {
			if _, err := manager.ExecuteStream(context.Background(), []string{"unregistered-provider"}, cliproxyexecutor.Request{Model: "test"}, opts); err == nil {
				t.Fatal("expected unregistered provider error")
			}
		} else if _, err := manager.Execute(context.Background(), []string{"unregistered-provider"}, cliproxyexecutor.Request{Model: "test"}, opts); err == nil {
			t.Fatal("expected unregistered provider error")
		}
	}
	if !reflect.DeepEqual(metadata, map[string]any{"caller-marker": "unchanged"}) {
		t.Errorf("other-provider metadata mutated: %v", metadata)
	}
}

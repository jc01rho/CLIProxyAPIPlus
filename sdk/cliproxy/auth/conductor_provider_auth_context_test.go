package auth

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerSetsProviderAuthContextForMixedExecutionPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		run  func(*Manager, context.Context, cliproxyexecutor.Request) error
	}{
		{
			name: "execute",
			run: func(manager *Manager, ctx context.Context, req cliproxyexecutor.Request) error {
				_, err := manager.Execute(ctx, []string{"codex"}, req, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "count tokens",
			run: func(manager *Manager, ctx context.Context, req cliproxyexecutor.Request) error {
				_, err := manager.ExecuteCount(ctx, []string{"codex"}, req, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "stream",
			run: func(manager *Manager, ctx context.Context, req cliproxyexecutor.Request) error {
				result, err := manager.ExecuteStream(ctx, []string{"codex"}, req, cliproxyexecutor.Options{})
				if err != nil {
					return err
				}
				for range result.Chunks {
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const (
				model     = "gpt-5.6-test"
				authID    = "auth-file-1.json"
				authLabel = "someone@example.com"
			)

			manager := NewManager(nil, &FillFirstSelector{}, nil)
			executor := &providerFallbackExecutor{id: "codex"}
			manager.RegisterExecutor(executor)
			auth := &Auth{ID: authID, Provider: "codex", Label: authLabel, Status: StatusActive}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatalf("register auth: %v", err)
			}

			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx := context.WithValue(context.Background(), "gin", ginCtx)

			if err := tt.run(manager, ctx, cliproxyexecutor.Request{Model: model}); err != nil {
				t.Fatalf("run %s: %v", tt.name, err)
			}

			value, exists := ginCtx.Get(GinProviderAuthKey)
			if !exists {
				t.Fatalf("%s did not set %q", tt.name, GinProviderAuthKey)
			}
			got, ok := value.(map[string]string)
			if !ok {
				t.Fatalf("%s provider auth value type = %T, want map[string]string", tt.name, value)
			}
			want := map[string]string{
				"provider":   "codex",
				"auth_id":    authID,
				"auth_label": authLabel,
			}
			for key, wantValue := range want {
				if got[key] != wantValue {
					t.Fatalf("%s provider auth %s = %q, want %q", tt.name, key, got[key], wantValue)
				}
			}
		})
	}
}

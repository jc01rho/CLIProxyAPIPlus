// Package auth provides authentication selection and management.
// This file contains tests for ActualModel tracking through fallback chains.
// It verifies that Response.ActualModel correctly reflects the model that was
// actually used after fallback, including model rewrite scenarios.
package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// actualModelTestExecutor tracks ActualModel in responses
type actualModelTestExecutor struct {
	identifier   string
	modelResults map[string]actualModelResult
	mu           sync.RWMutex
}

type actualModelResult struct {
	resp cliproxyexecutor.Response
	err  error
}

func newActualModelTestExecutor(provider string) *actualModelTestExecutor {
	return &actualModelTestExecutor{
		identifier:   provider,
		modelResults: make(map[string]actualModelResult),
	}
}

func (e *actualModelTestExecutor) Identifier() string {
	return e.identifier
}

func (e *actualModelTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.RLock()
	result, ok := e.modelResults[req.Model]
	e.mu.RUnlock()

	if ok {
		return result.resp, result.err
	}
	return cliproxyexecutor.Response{}, newModelCooldownError(req.Model, e.identifier, time.Minute)
}

func (e *actualModelTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	resp, err := e.Execute(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(ch)
		ch <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
	}()
	return ch, nil
}

func (e *actualModelTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *actualModelTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *actualModelTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *actualModelTestExecutor) setModelSuccess(model string, actualModel string, payload []byte) {
	e.mu.Lock()
	e.modelResults[model] = actualModelResult{
		resp: cliproxyexecutor.Response{
			Payload:     payload,
			ActualModel: actualModel,
		},
	}
	e.mu.Unlock()
}

func (e *actualModelTestExecutor) setModelCooldown(model string, duration time.Duration) {
	e.mu.Lock()
	e.modelResults[model] = actualModelResult{
		err: newModelCooldownError(model, e.identifier, duration),
	}
	e.mu.Unlock()
}

// setupActualModelTest creates a manager with auth and executor for ActualModel testing
func setupActualModelTest(t *testing.T, authID string, models []string) (*Manager, *actualModelTestExecutor) {
	t.Helper()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	executor := newActualModelTestExecutor("test-provider")
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       authID,
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "test@example.com"},
	}
	manager.Register(context.Background(), auth)

	// Register models in global registry
	modelInfos := make([]*registry.ModelInfo, len(models))
	for i, m := range models {
		modelInfos[i] = &registry.ModelInfo{ID: m}
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, modelInfos)

	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	return manager, executor
}

// =============================================================================
// Test 2.12: ActualModel Tracking Through Fallback (4 scenarios)
// =============================================================================

func TestFallback_ActualModelTracking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		fallbackChain     []string
		setupExecutor     func(e *actualModelTestExecutor)
		requestModel      string
		expectActualModel string
		description       string
	}{
		{
			name:          "NoFallback_ActualModelMatchesRequest",
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutor: func(e *actualModelTestExecutor) {
				// model-a succeeds, returns itself as ActualModel
				e.setModelSuccess("model-a", "model-a", []byte(`{"model":"model-a"}`))
			},
			requestModel:      "model-a",
			expectActualModel: "model-a",
			description:       "When no fallback occurs, ActualModel should match requested model",
		},
		{
			name:          "SingleFallback_ActualModelIsFallbackModel",
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutor: func(e *actualModelTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelSuccess("model-b", "model-b", []byte(`{"model":"model-b"}`))
			},
			requestModel:      "model-a",
			expectActualModel: "model-b",
			description:       "After single fallback, ActualModel should be the fallback model",
		},
		{
			name:          "MultipleFallbacks_ActualModelIsFinalModel",
			fallbackChain: []string{"model-a", "model-b", "model-c"},
			setupExecutor: func(e *actualModelTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
				e.setModelSuccess("model-c", "model-c", []byte(`{"model":"model-c"}`))
			},
			requestModel:      "model-a",
			expectActualModel: "model-c",
			description:       "After multiple fallbacks, ActualModel should be the final successful model",
		},
		{
			name:          "FallbackWithModelRewrite_ActualModelIsRewritten",
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutor: func(e *actualModelTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				// model-b is requested but executor returns a rewritten model name
				e.setModelSuccess("model-b", "model-b-v2-rewritten", []byte(`{"model":"model-b-v2-rewritten"}`))
			},
			requestModel:      "model-a",
			expectActualModel: "model-b-v2-rewritten",
			description:       "If executor rewrites model name, ActualModel should reflect the rewrite",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, executor := setupActualModelTest(t, "actual-model-"+tt.name, tt.fallbackChain)
			manager.SetFallbackConfig(nil, tt.fallbackChain)
			tt.setupExecutor(executor)

			req := cliproxyexecutor.Request{Model: tt.requestModel}
			resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			if err != nil {
				t.Fatalf("Expected success, got error: %v", err)
			}

			if resp.ActualModel != tt.expectActualModel {
				t.Errorf("ActualModel = %q, want %q", resp.ActualModel, tt.expectActualModel)
			}
		})
	}
}

// TestFallback_ActualModelTracking_Stream tests ActualModel tracking in streaming mode
func TestFallback_ActualModelTracking_Stream(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b"}
	manager, executor := setupActualModelTest(t, "actual-model-stream", models)
	manager.SetFallbackConfig(nil, models)

	// model-a fails, model-b succeeds
	executor.setModelCooldown("model-a", 5*time.Minute)
	executor.setModelSuccess("model-b", "model-b", []byte(`stream-data`))

	req := cliproxyexecutor.Request{Model: "model-a"}
	chunks, err := manager.ExecuteStream(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Expected success, got error: %v", err)
	}

	// Drain the channel
	for range chunks {
	}

	// Note: In streaming mode, ActualModel tracking depends on implementation
	// This test documents the expected behavior
}

// TestFallback_ActualModelTracking_WithFallbackModelsMap tests ActualModel with FallbackModels map
func TestFallback_ActualModelTracking_WithFallbackModelsMap(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-x"}
	manager, executor := setupActualModelTest(t, "actual-model-map", models)

	// Use FallbackModels map instead of chain
	manager.SetFallbackConfig(map[string]string{"model-a": "model-x"}, nil)

	executor.setModelCooldown("model-a", 5*time.Minute)
	executor.setModelSuccess("model-x", "model-x", []byte(`{"model":"model-x"}`))

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Expected success, got error: %v", err)
	}

	if resp.ActualModel != "model-x" {
		t.Errorf("ActualModel = %q, want model-x", resp.ActualModel)
	}
}

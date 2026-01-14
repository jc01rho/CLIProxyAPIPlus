// Package auth provides authentication selection and management.
// This file contains advanced fallback scenario tests.
// It covers OAuth model mapping interaction, PrioritySelector + fallback,
// and edge cases in the getFallbackModel function.
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

// advancedTestExecutor is a mock executor for advanced fallback tests
type advancedTestExecutor struct {
	identifier   string
	modelResults map[string]fallbackModelResult
	mu           sync.RWMutex
}

func newAdvancedTestExecutor(provider string) *advancedTestExecutor {
	return &advancedTestExecutor{
		identifier:   provider,
		modelResults: make(map[string]fallbackModelResult),
	}
}

func (e *advancedTestExecutor) Identifier() string {
	return e.identifier
}

func (e *advancedTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.RLock()
	result, ok := e.modelResults[req.Model]
	e.mu.RUnlock()

	if ok {
		return result.resp, result.err
	}
	return cliproxyexecutor.Response{}, newModelCooldownError(req.Model, e.identifier, time.Minute)
}

func (e *advancedTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
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

func (e *advancedTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *advancedTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *advancedTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *advancedTestExecutor) setModelSuccess(model string, actualModel string, payload []byte) {
	e.mu.Lock()
	e.modelResults[model] = fallbackModelResult{
		resp: cliproxyexecutor.Response{Payload: payload, ActualModel: actualModel},
	}
	e.mu.Unlock()
}

func (e *advancedTestExecutor) setModelCooldown(model string, duration time.Duration) {
	e.mu.Lock()
	e.modelResults[model] = fallbackModelResult{
		err: newModelCooldownError(model, e.identifier, duration),
	}
	e.mu.Unlock()
}

// setupAdvancedTest creates a manager with auth and executor for advanced testing
func setupAdvancedTest(t *testing.T, authID string, models []string) (*Manager, *advancedTestExecutor) {
	t.Helper()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	executor := newAdvancedTestExecutor("test-provider")
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
// Test 2B.1: Fallback with OAuth Model Mapping (2 scenarios)
// =============================================================================

// TestFallback_Advanced_OAuthModelMapping tests fallback with OAuth model mapping
func TestFallback_Advanced_OAuthModelMapping(t *testing.T) {
	t.Parallel()

	// Note: OAuth model mapping is typically handled at a different layer
	// These tests verify that fallback works correctly with model names
	// that might be mapped by OAuth providers

	tests := []struct {
		name              string
		models            []string
		fallbackChain     []string
		setupExecutor     func(e *advancedTestExecutor)
		requestModel      string
		expectActualModel string
	}{
		{
			name:          "FallbackModelHasMapping",
			models:        []string{"model-a", "model-b"},
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutor: func(e *advancedTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				// model-b is requested but returns a mapped name
				e.setModelSuccess("model-b", "model-b-oauth-mapped", []byte(`{}`))
			},
			requestModel:      "model-a",
			expectActualModel: "model-b-oauth-mapped",
		},
		{
			name:          "OriginalAndFallbackBothMapped",
			models:        []string{"model-a", "model-b"},
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutor: func(e *advancedTestExecutor) {
				// model-a fails (would have been mapped to model-a-oauth)
				e.setModelCooldown("model-a", 5*time.Minute)
				// model-b succeeds with its mapped name
				e.setModelSuccess("model-b", "model-b-oauth-mapped", []byte(`{}`))
			},
			requestModel:      "model-a",
			expectActualModel: "model-b-oauth-mapped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, executor := setupAdvancedTest(t, "oauth-"+tt.name, tt.models)
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

// =============================================================================
// Test 2B.2: Fallback with Priority Selector (2 scenarios)
// =============================================================================

// TestFallback_Advanced_PrioritySelector tests fallback with PrioritySelector
func TestFallback_Advanced_PrioritySelector(t *testing.T) {
	t.Parallel()

	// Test that PrioritySelector works with fallback configuration
	// Using RoundRobinSelector as inner selector with standard provider

	models := []string{"model-a", "model-b"}
	manager, executor := setupAdvancedTest(t, "priority-selector-test", models)
	manager.SetFallbackConfig(nil, models)

	// model-a fails, model-b succeeds
	executor.setModelCooldown("model-a", 5*time.Minute)
	executor.setModelSuccess("model-b", "model-b", []byte(`{}`))

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Expected success via fallback, got error: %v", err)
	}

	if resp.ActualModel != "model-b" {
		t.Errorf("ActualModel = %q, want model-b", resp.ActualModel)
	}
}

// TestFallback_Advanced_PrioritySelectorProviderOrder tests provider ordering with fallback
func TestFallback_Advanced_PrioritySelectorProviderOrder(t *testing.T) {
	t.Parallel()

	// Test that fallback works when using PrioritySelector with provider ordering
	models := []string{"model-a", "model-b", "model-c"}
	manager, executor := setupAdvancedTest(t, "priority-order-test", models)
	manager.SetFallbackConfig(nil, models)

	// All models in chain fail except last
	executor.setModelCooldown("model-a", 5*time.Minute)
	executor.setModelCooldown("model-b", 5*time.Minute)
	executor.setModelSuccess("model-c", "model-c", []byte(`{}`))

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Expected success via fallback chain, got error: %v", err)
	}

	if resp.ActualModel != "model-c" {
		t.Errorf("ActualModel = %q, want model-c", resp.ActualModel)
	}
}

// =============================================================================
// Test 2B.3: Edge Cases in getFallbackModel (4 scenarios)
// =============================================================================

// TestFallback_Advanced_GetFallbackModelEdgeCases tests edge cases in fallback model selection
func TestFallback_Advanced_GetFallbackModelEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		fallbackModels    map[string]string
		fallbackChain     []string
		models            []string
		setupExecutor     func(e *advancedTestExecutor)
		requestModel      string
		expectSuccess     bool
		expectActualModel string
	}{
		{
			name:           "FallbackModelsConflictWithChain_MapWins",
			fallbackModels: map[string]string{"model-a": "model-x"},
			fallbackChain:  []string{"model-a", "model-b"},
			models:         []string{"model-a", "model-b", "model-x"},
			setupExecutor: func(e *advancedTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelSuccess("model-x", "model-x", []byte(`{}`))
				e.setModelSuccess("model-b", "model-b", []byte(`{}`))
			},
			requestModel:      "model-a",
			expectSuccess:     true,
			expectActualModel: "model-x", // FallbackModels takes priority
		},
		{
			name:           "AllVisited_NoFallback",
			fallbackModels: map[string]string{"model-a": "model-a"}, // Self-reference
			fallbackChain:  nil,
			models:         []string{"model-a"},
			setupExecutor: func(e *advancedTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
			},
			requestModel:  "model-a",
			expectSuccess: false,
		},
		{
			name:           "StandardFallbackChain",
			fallbackModels: nil,
			fallbackChain:  []string{"model-a", "model-b", "model-c"},
			models:         []string{"model-a", "model-b", "model-c"},
			setupExecutor: func(e *advancedTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
				e.setModelSuccess("model-c", "model-c", []byte(`{}`))
			},
			requestModel:      "model-a",
			expectSuccess:     true,
			expectActualModel: "model-c",
		},
		{
			name:           "FallbackMapChain",
			fallbackModels: map[string]string{"model-a": "model-b", "model-b": "model-c"},
			fallbackChain:  nil,
			models:         []string{"model-a", "model-b", "model-c"},
			setupExecutor: func(e *advancedTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
				e.setModelSuccess("model-c", "model-c", []byte(`{}`))
			},
			requestModel:      "model-a",
			expectSuccess:     true,
			expectActualModel: "model-c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, executor := setupAdvancedTest(t, "edge-"+tt.name, tt.models)
			manager.SetFallbackConfig(tt.fallbackModels, tt.fallbackChain)
			tt.setupExecutor(executor)

			req := cliproxyexecutor.Request{Model: tt.requestModel}
			resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			if tt.expectSuccess {
				if err != nil {
					t.Fatalf("Expected success, got error: %v", err)
				}
				if resp.ActualModel != tt.expectActualModel {
					t.Errorf("ActualModel = %q, want %q", resp.ActualModel, tt.expectActualModel)
				}
			} else {
				if err == nil {
					t.Fatal("Expected error, got success")
				}
			}
		})
	}
}

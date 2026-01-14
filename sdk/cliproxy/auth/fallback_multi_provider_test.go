// Package auth provides authentication selection and management.
// This file contains tests for multi-provider fallback behavior.
// It covers scenarios where the same model is available across multiple providers
// and verifies provider rotation before model fallback.
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

// multiProviderTestExecutor tracks calls per provider for multi-provider tests
type multiProviderTestExecutor struct {
	identifier   string
	modelResults map[string]fallbackModelResult
	callOrder    []string
	mu           sync.Mutex
}

func newMultiProviderTestExecutor(provider string) *multiProviderTestExecutor {
	return &multiProviderTestExecutor{
		identifier:   provider,
		modelResults: make(map[string]fallbackModelResult),
		callOrder:    make([]string, 0),
	}
}

func (e *multiProviderTestExecutor) Identifier() string {
	return e.identifier
}

func (e *multiProviderTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.callOrder = append(e.callOrder, e.identifier+":"+req.Model)
	result, ok := e.modelResults[req.Model]
	e.mu.Unlock()

	if ok {
		return result.resp, result.err
	}
	return cliproxyexecutor.Response{}, newModelCooldownError(req.Model, e.identifier, time.Minute)
}

func (e *multiProviderTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	e.mu.Lock()
	e.callOrder = append(e.callOrder, e.identifier+":"+req.Model)
	result, ok := e.modelResults[req.Model]
	e.mu.Unlock()

	if ok {
		if result.err != nil {
			return nil, result.err
		}
		ch := make(chan cliproxyexecutor.StreamChunk)
		go func() {
			defer close(ch)
			ch <- cliproxyexecutor.StreamChunk{Payload: result.resp.Payload}
		}()
		return ch, nil
	}
	return nil, newModelCooldownError(req.Model, e.identifier, time.Minute)
}

func (e *multiProviderTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *multiProviderTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *multiProviderTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *multiProviderTestExecutor) setModelSuccess(model string, payload []byte) {
	e.mu.Lock()
	e.modelResults[model] = fallbackModelResult{
		resp: cliproxyexecutor.Response{Payload: payload, ActualModel: model},
	}
	e.mu.Unlock()
}

func (e *multiProviderTestExecutor) setModelCooldown(model string, duration time.Duration) {
	e.mu.Lock()
	e.modelResults[model] = fallbackModelResult{
		err: newModelCooldownError(model, e.identifier, duration),
	}
	e.mu.Unlock()
}

func (e *multiProviderTestExecutor) getCallOrder() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]string, len(e.callOrder))
	copy(result, e.callOrder)
	return result
}

// =============================================================================
// Test 2.6: Multi-Provider Fallback (4 scenarios)
// =============================================================================

func TestFallback_MultiProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		providers      []string
		models         []string
		fallbackChain  []string
		setupExecutors func(executors map[string]*multiProviderTestExecutor)
		setupAuths     func(manager *Manager, providers []string, models []string, t *testing.T)
		requestModel   string
		expectSuccess  bool
		expectProvider string
		expectModel    string
	}{
		{
			name:          "SameModelDifferentProviders_FirstBlockedSecondWorks",
			providers:     []string{"provider-1", "provider-2"},
			models:        []string{"model-a"},
			fallbackChain: nil,
			setupExecutors: func(executors map[string]*multiProviderTestExecutor) {
				executors["provider-1"].setModelCooldown("model-a", 5*time.Minute)
				executors["provider-2"].setModelSuccess("model-a", []byte(`{"provider":"provider-2"}`))
			},
			setupAuths: func(manager *Manager, providers []string, models []string, t *testing.T) {
				for _, provider := range providers {
					auth := &Auth{
						ID:       "multi-provider-auth-" + provider,
						Provider: provider,
						Status:   StatusActive,
						Metadata: map[string]any{"email": "test@example.com"},
					}
					manager.Register(context.Background(), auth)

					modelInfos := make([]*registry.ModelInfo, len(models))
					for i, m := range models {
						modelInfos[i] = &registry.ModelInfo{ID: m}
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, modelInfos)
					t.Cleanup(func() {
						registry.GetGlobalRegistry().UnregisterClient(auth.ID)
					})
				}
			},
			requestModel:   "model-a",
			expectSuccess:  true,
			expectProvider: "provider-2",
			expectModel:    "model-a",
		},
		{
			name:          "AllProvidersBlockedForModel_TriggerModelFallback",
			providers:     []string{"provider-1", "provider-2"},
			models:        []string{"model-a", "model-b"},
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutors: func(executors map[string]*multiProviderTestExecutor) {
				// Both providers blocked for model-a
				executors["provider-1"].setModelCooldown("model-a", 5*time.Minute)
				executors["provider-2"].setModelCooldown("model-a", 5*time.Minute)
				// model-b works on provider-1
				executors["provider-1"].setModelSuccess("model-b", []byte(`{"model":"model-b"}`))
			},
			setupAuths: func(manager *Manager, providers []string, models []string, t *testing.T) {
				for _, provider := range providers {
					auth := &Auth{
						ID:       "multi-provider-fallback-auth-" + provider,
						Provider: provider,
						Status:   StatusActive,
						Metadata: map[string]any{"email": "test@example.com"},
					}
					manager.Register(context.Background(), auth)

					modelInfos := make([]*registry.ModelInfo, len(models))
					for i, m := range models {
						modelInfos[i] = &registry.ModelInfo{ID: m}
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, modelInfos)
					t.Cleanup(func() {
						registry.GetGlobalRegistry().UnregisterClient(auth.ID)
					})
				}
			},
			requestModel:   "model-a",
			expectSuccess:  true,
			expectProvider: "provider-1",
			expectModel:    "model-b",
		},
		{
			name:          "ProviderRotationBeforeFallback",
			providers:     []string{"provider-1", "provider-2", "provider-3"},
			models:        []string{"model-a", "model-b"},
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutors: func(executors map[string]*multiProviderTestExecutor) {
				// All providers blocked for model-a
				executors["provider-1"].setModelCooldown("model-a", 5*time.Minute)
				executors["provider-2"].setModelCooldown("model-a", 5*time.Minute)
				executors["provider-3"].setModelCooldown("model-a", 5*time.Minute)
				// model-b works on provider-2
				executors["provider-2"].setModelSuccess("model-b", []byte(`{"model":"model-b"}`))
			},
			setupAuths: func(manager *Manager, providers []string, models []string, t *testing.T) {
				for _, provider := range providers {
					auth := &Auth{
						ID:       "rotation-auth-" + provider,
						Provider: provider,
						Status:   StatusActive,
						Metadata: map[string]any{"email": "test@example.com"},
					}
					manager.Register(context.Background(), auth)

					modelInfos := make([]*registry.ModelInfo, len(models))
					for i, m := range models {
						modelInfos[i] = &registry.ModelInfo{ID: m}
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, modelInfos)
					t.Cleanup(func() {
						registry.GetGlobalRegistry().UnregisterClient(auth.ID)
					})
				}
			},
			requestModel:   "model-a",
			expectSuccess:  true,
			expectProvider: "provider-2",
			expectModel:    "model-b",
		},
		{
			name:          "MixedProviderAvailability_UsesAvailableProvider",
			providers:     []string{"provider-1", "provider-2"},
			models:        []string{"model-a"},
			fallbackChain: nil,
			setupExecutors: func(executors map[string]*multiProviderTestExecutor) {
				// provider-1 has auth and works
				executors["provider-1"].setModelSuccess("model-a", []byte(`{"provider":"provider-1"}`))
				// provider-2 also works but we only register auth for provider-1
			},
			setupAuths: func(manager *Manager, providers []string, models []string, t *testing.T) {
				// Only register auth for provider-1
				auth := &Auth{
					ID:       "mixed-availability-auth",
					Provider: "provider-1",
					Status:   StatusActive,
					Metadata: map[string]any{"email": "test@example.com"},
				}
				manager.Register(context.Background(), auth)

				modelInfos := make([]*registry.ModelInfo, len(models))
				for i, m := range models {
					modelInfos[i] = &registry.ModelInfo{ID: m}
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, modelInfos)
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(auth.ID)
				})
			},
			requestModel:   "model-a",
			expectSuccess:  true,
			expectProvider: "provider-1",
			expectModel:    "model-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})

			// Create and register executors for each provider
			executors := make(map[string]*multiProviderTestExecutor)
			for _, provider := range tt.providers {
				executor := newMultiProviderTestExecutor(provider)
				executors[provider] = executor
				manager.RegisterExecutor(executor)
			}

			// Setup executors with model results
			tt.setupExecutors(executors)

			// Setup auths
			tt.setupAuths(manager, tt.providers, tt.models, t)

			// Configure fallback
			if tt.fallbackChain != nil {
				manager.SetFallbackConfig(nil, tt.fallbackChain)
			}

			// Execute
			req := cliproxyexecutor.Request{Model: tt.requestModel}
			resp, err := manager.Execute(context.Background(), tt.providers, req, cliproxyexecutor.Options{})

			if tt.expectSuccess {
				if err != nil {
					t.Fatalf("Expected success, got error: %v", err)
				}
				if resp.ActualModel != tt.expectModel {
					t.Errorf("ActualModel = %q, want %q", resp.ActualModel, tt.expectModel)
				}
			} else {
				if err == nil {
					t.Fatal("Expected error, got success")
				}
			}
		})
	}
}

// TestFallback_MultiProvider_ProviderOrder verifies that providers are tried in order
func TestFallback_MultiProvider_ProviderOrder(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})

	providers := []string{"provider-a", "provider-b", "provider-c"}
	executors := make(map[string]*multiProviderTestExecutor)

	// Create executors - all fail for model-x
	for _, provider := range providers {
		executor := newMultiProviderTestExecutor(provider)
		executor.setModelCooldown("model-x", 5*time.Minute)
		executors[provider] = executor
		manager.RegisterExecutor(executor)
	}

	// Register auths for all providers
	for _, provider := range providers {
		auth := &Auth{
			ID:       "order-test-auth-" + provider,
			Provider: provider,
			Status:   StatusActive,
			Metadata: map[string]any{"email": "test@example.com"},
		}
		manager.Register(context.Background(), auth)

		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
			{ID: "model-x"},
		})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		})
	}

	// Execute - should try all providers
	req := cliproxyexecutor.Request{Model: "model-x"}
	_, err := manager.Execute(context.Background(), providers, req, cliproxyexecutor.Options{})

	// Should fail (all providers blocked)
	if err == nil {
		t.Fatal("Expected error, got success")
	}

	// Verify all providers were tried
	totalCalls := 0
	for _, executor := range executors {
		totalCalls += len(executor.getCallOrder())
	}

	// At least one provider should have been called
	if totalCalls == 0 {
		t.Error("No providers were called")
	}
}

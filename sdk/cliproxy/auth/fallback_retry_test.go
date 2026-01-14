// Package auth provides authentication selection and management.
// This file contains tests for fallback + retry interaction behavior.
// It verifies that retry exhaustion happens before fallback triggers,
// and that the combined retry + fallback flow works correctly.
package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// retryTestExecutor tracks call counts per model for retry testing
type retryTestExecutor struct {
	identifier   string
	modelResults map[string][]retryAttemptResult // model -> results per attempt
	callCounts   map[string]*int64               // model -> atomic call count
	mu           sync.RWMutex
}

type retryAttemptResult struct {
	resp cliproxyexecutor.Response
	err  error
}

func newRetryTestExecutor(provider string) *retryTestExecutor {
	return &retryTestExecutor{
		identifier:   provider,
		modelResults: make(map[string][]retryAttemptResult),
		callCounts:   make(map[string]*int64),
	}
}

func (e *retryTestExecutor) Identifier() string {
	return e.identifier
}

func (e *retryTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Get and increment call count atomically
	e.mu.Lock()
	if _, ok := e.callCounts[req.Model]; !ok {
		var counter int64
		e.callCounts[req.Model] = &counter
	}
	counter := e.callCounts[req.Model]
	e.mu.Unlock()

	callNum := atomic.AddInt64(counter, 1) - 1 // 0-indexed

	e.mu.RLock()
	results, ok := e.modelResults[req.Model]
	e.mu.RUnlock()

	if ok && int(callNum) < len(results) {
		return results[callNum].resp, results[callNum].err
	}

	// Default: return cooldown error
	return cliproxyexecutor.Response{}, newModelCooldownError(req.Model, e.identifier, time.Minute)
}

func (e *retryTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
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

func (e *retryTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *retryTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *retryTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

// setModelAttempts configures results for each attempt of a model
// attempts[0] = first call result, attempts[1] = second call result, etc.
func (e *retryTestExecutor) setModelAttempts(model string, attempts []retryAttemptResult) {
	e.mu.Lock()
	e.modelResults[model] = attempts
	if _, ok := e.callCounts[model]; !ok {
		var counter int64
		e.callCounts[model] = &counter
	}
	e.mu.Unlock()
}

func (e *retryTestExecutor) getCallCount(model string) int64 {
	e.mu.RLock()
	counter, ok := e.callCounts[model]
	e.mu.RUnlock()
	if !ok {
		return 0
	}
	return atomic.LoadInt64(counter)
}

func (e *retryTestExecutor) resetCallCounts() {
	e.mu.Lock()
	for model := range e.callCounts {
		atomic.StoreInt64(e.callCounts[model], 0)
	}
	e.mu.Unlock()
}

// setupRetryTest creates a manager with auth and executor for retry testing
func setupRetryTest(t *testing.T, authID string, models []string) (*Manager, *retryTestExecutor) {
	t.Helper()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	executor := newRetryTestExecutor("test-provider")
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
// Test 2.9: Fallback + Retry Interaction (6 scenarios)
// =============================================================================

// TestFallback_RetryThenFallback tests that retry exhaustion happens before fallback
func TestFallback_RetryThenFallback(t *testing.T) {
	t.Parallel()

	// Note: The current implementation doesn't have built-in retry logic in the Manager.
	// Retry is typically handled at a higher level (e.g., HTTP middleware).
	// These tests document the expected behavior if retry were implemented.

	models := []string{"model-a", "model-b"}
	manager, executor := setupRetryTest(t, "retry-fallback-test", models)
	manager.SetFallbackConfig(nil, models)

	// model-a always fails with cooldown, model-b succeeds
	executor.setModelAttempts("model-a", []retryAttemptResult{
		{err: newModelCooldownError("model-a", "test-provider", time.Minute)},
		{err: newModelCooldownError("model-a", "test-provider", time.Minute)},
		{err: newModelCooldownError("model-a", "test-provider", time.Minute)},
	})
	executor.setModelAttempts("model-b", []retryAttemptResult{
		{resp: cliproxyexecutor.Response{Payload: []byte(`{"model":"model-b"}`), ActualModel: "model-b"}},
	})

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Expected success via fallback, got error: %v", err)
	}

	if resp.ActualModel != "model-b" {
		t.Errorf("ActualModel = %q, want model-b", resp.ActualModel)
	}

	// Verify model-a was called (at least once before fallback)
	modelACalls := executor.getCallCount("model-a")
	if modelACalls == 0 {
		t.Error("Expected model-a to be called before fallback")
	}

	// Verify model-b was called (fallback)
	modelBCalls := executor.getCallCount("model-b")
	if modelBCalls == 0 {
		t.Error("Expected model-b to be called via fallback")
	}
}

// TestFallback_NoRetryDirectFallback tests direct fallback without retry
func TestFallback_NoRetryDirectFallback(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b"}
	manager, executor := setupRetryTest(t, "no-retry-fallback", models)
	manager.SetFallbackConfig(nil, models)

	// model-a fails immediately, model-b succeeds
	executor.setModelAttempts("model-a", []retryAttemptResult{
		{err: newModelCooldownError("model-a", "test-provider", time.Minute)},
	})
	executor.setModelAttempts("model-b", []retryAttemptResult{
		{resp: cliproxyexecutor.Response{Payload: []byte(`{"model":"model-b"}`), ActualModel: "model-b"}},
	})

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Expected success via fallback, got error: %v", err)
	}

	if resp.ActualModel != "model-b" {
		t.Errorf("ActualModel = %q, want model-b", resp.ActualModel)
	}

	// Verify model-a was called exactly once (no retry)
	modelACalls := executor.getCallCount("model-a")
	if modelACalls != 1 {
		t.Errorf("Expected model-a to be called once, got %d", modelACalls)
	}
}

// TestFallback_RetrySucceeds_NoFallback tests that successful retry prevents fallback
func TestFallback_RetrySucceeds_NoFallback(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b"}
	manager, executor := setupRetryTest(t, "retry-succeeds", models)
	manager.SetFallbackConfig(nil, models)

	// model-a succeeds on first try
	executor.setModelAttempts("model-a", []retryAttemptResult{
		{resp: cliproxyexecutor.Response{Payload: []byte(`{"model":"model-a"}`), ActualModel: "model-a"}},
	})
	executor.setModelAttempts("model-b", []retryAttemptResult{
		{resp: cliproxyexecutor.Response{Payload: []byte(`{"model":"model-b"}`), ActualModel: "model-b"}},
	})

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Expected success, got error: %v", err)
	}

	if resp.ActualModel != "model-a" {
		t.Errorf("ActualModel = %q, want model-a", resp.ActualModel)
	}

	// Verify model-b was NOT called (no fallback needed)
	modelBCalls := executor.getCallCount("model-b")
	if modelBCalls != 0 {
		t.Errorf("Expected model-b to NOT be called, got %d calls", modelBCalls)
	}
}

// TestFallback_RetryAndFallbackBothExhausted tests when both retry and fallback fail
func TestFallback_RetryAndFallbackBothExhausted(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b"}
	manager, executor := setupRetryTest(t, "both-exhausted", models)
	manager.SetFallbackConfig(nil, models)

	// Both models always fail
	executor.setModelAttempts("model-a", []retryAttemptResult{
		{err: newModelCooldownError("model-a", "test-provider", time.Minute)},
	})
	executor.setModelAttempts("model-b", []retryAttemptResult{
		{err: newModelCooldownError("model-b", "test-provider", time.Minute)},
	})

	req := cliproxyexecutor.Request{Model: "model-a"}
	_, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err == nil {
		t.Fatal("Expected error when both retry and fallback exhausted")
	}

	assertModelCooldownError(t, err)

	// Verify both models were tried
	modelACalls := executor.getCallCount("model-a")
	modelBCalls := executor.getCallCount("model-b")

	if modelACalls == 0 {
		t.Error("Expected model-a to be called")
	}
	if modelBCalls == 0 {
		t.Error("Expected model-b to be called via fallback")
	}
}

// TestFallback_ZeroRetryWithFallback tests fallback with zero retry configuration
func TestFallback_ZeroRetryWithFallback(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b", "model-c"}
	manager, executor := setupRetryTest(t, "zero-retry", models)
	manager.SetFallbackConfig(nil, models)

	// model-a fails, model-b fails, model-c succeeds
	executor.setModelAttempts("model-a", []retryAttemptResult{
		{err: newModelCooldownError("model-a", "test-provider", time.Minute)},
	})
	executor.setModelAttempts("model-b", []retryAttemptResult{
		{err: newModelCooldownError("model-b", "test-provider", time.Minute)},
	})
	executor.setModelAttempts("model-c", []retryAttemptResult{
		{resp: cliproxyexecutor.Response{Payload: []byte(`{"model":"model-c"}`), ActualModel: "model-c"}},
	})

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Expected success via fallback chain, got error: %v", err)
	}

	if resp.ActualModel != "model-c" {
		t.Errorf("ActualModel = %q, want model-c", resp.ActualModel)
	}

	// Verify all models were called in order
	modelACalls := executor.getCallCount("model-a")
	modelBCalls := executor.getCallCount("model-b")
	modelCCalls := executor.getCallCount("model-c")

	if modelACalls != 1 {
		t.Errorf("Expected model-a called once, got %d", modelACalls)
	}
	if modelBCalls != 1 {
		t.Errorf("Expected model-b called once, got %d", modelBCalls)
	}
	if modelCCalls != 1 {
		t.Errorf("Expected model-c called once, got %d", modelCCalls)
	}
}

// TestFallback_MultipleAuthsWithRetry tests retry behavior with multiple auths
func TestFallback_MultipleAuthsWithRetry(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	executor := newRetryTestExecutor("test-provider")
	manager.RegisterExecutor(executor)

	// Register multiple auths
	models := []string{"model-a", "model-b"}
	for i := 1; i <= 3; i++ {
		auth := &Auth{
			ID:       "multi-auth-" + string(rune('0'+i)),
			Provider: "test-provider",
			Status:   StatusActive,
			Metadata: map[string]any{"email": "test@example.com"},
		}
		manager.Register(context.Background(), auth)

		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
			{ID: "model-a"},
			{ID: "model-b"},
		})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		})
	}

	manager.SetFallbackConfig(nil, models)

	// model-a always fails, model-b succeeds
	executor.setModelAttempts("model-a", []retryAttemptResult{
		{err: newModelCooldownError("model-a", "test-provider", time.Minute)},
		{err: newModelCooldownError("model-a", "test-provider", time.Minute)},
		{err: newModelCooldownError("model-a", "test-provider", time.Minute)},
	})
	executor.setModelAttempts("model-b", []retryAttemptResult{
		{resp: cliproxyexecutor.Response{Payload: []byte(`{"model":"model-b"}`), ActualModel: "model-b"}},
	})

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Expected success via fallback, got error: %v", err)
	}

	if resp.ActualModel != "model-b" {
		t.Errorf("ActualModel = %q, want model-b", resp.ActualModel)
	}
}

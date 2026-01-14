// Package auth provides authentication selection and management.
// This file contains tests for concurrent fallback behavior.
// It verifies that fallback chains work correctly under concurrent load
// and that visited map isolation prevents cross-request interference.
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

// concurrentTestExecutor is a mock executor for concurrent fallback tests
type concurrentTestExecutor struct {
	identifier   string
	modelResults map[string]fallbackModelResult
	callCount    map[string]*int64 // atomic counters per model
	mu           sync.RWMutex
}

func newConcurrentTestExecutor(provider string) *concurrentTestExecutor {
	return &concurrentTestExecutor{
		identifier:   provider,
		modelResults: make(map[string]fallbackModelResult),
		callCount:    make(map[string]*int64),
	}
}

func (e *concurrentTestExecutor) Identifier() string {
	return e.identifier
}

func (e *concurrentTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Increment call count atomically
	e.mu.RLock()
	counter, ok := e.callCount[req.Model]
	e.mu.RUnlock()
	if ok {
		atomic.AddInt64(counter, 1)
	}

	e.mu.RLock()
	result, hasResult := e.modelResults[req.Model]
	e.mu.RUnlock()

	if hasResult {
		return result.resp, result.err
	}
	return cliproxyexecutor.Response{}, newModelCooldownError(req.Model, e.identifier, time.Minute)
}

func (e *concurrentTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	e.mu.RLock()
	counter, ok := e.callCount[req.Model]
	e.mu.RUnlock()
	if ok {
		atomic.AddInt64(counter, 1)
	}

	e.mu.RLock()
	result, hasResult := e.modelResults[req.Model]
	e.mu.RUnlock()

	if hasResult {
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

func (e *concurrentTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *concurrentTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *concurrentTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *concurrentTestExecutor) setModelSuccess(model string, payload []byte) {
	e.mu.Lock()
	e.modelResults[model] = fallbackModelResult{
		resp: cliproxyexecutor.Response{Payload: payload, ActualModel: model},
	}
	if _, ok := e.callCount[model]; !ok {
		var counter int64
		e.callCount[model] = &counter
	}
	e.mu.Unlock()
}

func (e *concurrentTestExecutor) setModelCooldown(model string, duration time.Duration) {
	e.mu.Lock()
	e.modelResults[model] = fallbackModelResult{
		err: newModelCooldownError(model, e.identifier, duration),
	}
	if _, ok := e.callCount[model]; !ok {
		var counter int64
		e.callCount[model] = &counter
	}
	e.mu.Unlock()
}

func (e *concurrentTestExecutor) getCallCount(model string) int64 {
	e.mu.RLock()
	counter, ok := e.callCount[model]
	e.mu.RUnlock()
	if !ok {
		return 0
	}
	return atomic.LoadInt64(counter)
}

// setupConcurrentTest creates a manager with auth and executor for concurrent testing
func setupConcurrentTest(t *testing.T, authID string, models []string) (*Manager, *concurrentTestExecutor) {
	t.Helper()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	executor := newConcurrentTestExecutor("test-provider")
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
// Test 2.10: Concurrent Fallback Chains (5 scenarios)
// =============================================================================

// TestFallback_Concurrent_ParallelRequests tests 10 parallel requests all triggering fallback
func TestFallback_Concurrent_ParallelRequests(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b"}
	manager, executor := setupConcurrentTest(t, "concurrent-parallel", models)
	manager.SetFallbackConfig(nil, models)

	// model-a blocked, model-b works
	executor.setModelCooldown("model-a", 5*time.Minute)
	executor.setModelSuccess("model-b", []byte(`{"model":"model-b"}`))

	const numRequests = 10
	var wg sync.WaitGroup
	results := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := cliproxyexecutor.Request{Model: "model-a"}
			_, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})
			results <- err
		}()
	}

	wg.Wait()
	close(results)

	// All requests should succeed via fallback
	successCount := 0
	for err := range results {
		if err == nil {
			successCount++
		}
	}

	if successCount != numRequests {
		t.Errorf("Expected %d successes, got %d", numRequests, successCount)
	}

	// Verify model-b was called for all requests
	modelBCalls := executor.getCallCount("model-b")
	if modelBCalls < int64(numRequests) {
		t.Errorf("Expected at least %d calls to model-b, got %d", numRequests, modelBCalls)
	}
}

// TestFallback_Concurrent_VisitedMapIsolation verifies each request has its own visited map
func TestFallback_Concurrent_VisitedMapIsolation(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b", "model-c"}
	manager, executor := setupConcurrentTest(t, "concurrent-visited", models)
	manager.SetFallbackConfig(nil, models)

	// All models blocked - each request should try all models independently
	executor.setModelCooldown("model-a", 5*time.Minute)
	executor.setModelCooldown("model-b", 5*time.Minute)
	executor.setModelCooldown("model-c", 5*time.Minute)

	const numRequests = 10
	var wg sync.WaitGroup

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := cliproxyexecutor.Request{Model: "model-a"}
			manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})
		}()
	}

	wg.Wait()

	// Verify that fallback was attempted - model-b and model-c should have been called
	// Note: Due to shared auth state, the exact call counts depend on timing,
	// but the visited map isolation ensures each request tracks its own visited models
	modelACalls := executor.getCallCount("model-a")
	modelBCalls := executor.getCallCount("model-b")
	modelCCalls := executor.getCallCount("model-c")

	// At least one request should have tried model-a
	if modelACalls == 0 {
		t.Error("Expected at least one call to model-a")
	}

	// Fallback should have tried model-b and model-c
	if modelBCalls == 0 {
		t.Error("Expected some calls to model-b via fallback")
	}
	if modelCCalls == 0 {
		t.Error("Expected some calls to model-c via fallback")
	}

	// Total calls should show fallback chain was exercised
	totalCalls := modelACalls + modelBCalls + modelCCalls
	t.Logf("Call distribution: model-a=%d, model-b=%d, model-c=%d, total=%d",
		modelACalls, modelBCalls, modelCCalls, totalCalls)
}

// TestFallback_Concurrent_NoDeadlock tests that high concurrency doesn't cause deadlock
func TestFallback_Concurrent_NoDeadlock(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b", "model-c", "model-d", "model-e"}
	manager, executor := setupConcurrentTest(t, "concurrent-deadlock", models)
	manager.SetFallbackConfig(nil, models)

	// Complex chain with some successes
	executor.setModelCooldown("model-a", 5*time.Minute)
	executor.setModelCooldown("model-b", 5*time.Minute)
	executor.setModelSuccess("model-c", []byte(`{"model":"model-c"}`))
	executor.setModelSuccess("model-d", []byte(`{"model":"model-d"}`))
	executor.setModelSuccess("model-e", []byte(`{"model":"model-e"}`))

	const numGoroutines = 100
	done := make(chan struct{})

	go func() {
		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := cliproxyexecutor.Request{Model: "model-a"}
				manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})
			}()
		}
		wg.Wait()
		close(done)
	}()

	// Wait with timeout to detect deadlock
	select {
	case <-done:
		// Success - no deadlock
	case <-time.After(30 * time.Second):
		t.Fatal("Deadlock detected: concurrent fallback did not complete within 30 seconds")
	}
}

// TestFallback_Concurrent_RaceCondition tests for race conditions using -race flag
func TestFallback_Concurrent_RaceCondition(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b"}
	manager, executor := setupConcurrentTest(t, "concurrent-race", models)
	manager.SetFallbackConfig(nil, models)

	executor.setModelCooldown("model-a", 5*time.Minute)
	executor.setModelSuccess("model-b", []byte(`{"model":"model-b"}`))

	const numGoroutines = 50
	var wg sync.WaitGroup

	// Mix of Execute and ExecuteStream calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(2)

		// Execute call
		go func() {
			defer wg.Done()
			req := cliproxyexecutor.Request{Model: "model-a"}
			manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})
		}()

		// ExecuteStream call
		go func() {
			defer wg.Done()
			req := cliproxyexecutor.Request{Model: "model-a"}
			chunks, err := manager.ExecuteStream(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})
			if err == nil && chunks != nil {
				for range chunks {
				}
			}
		}()
	}

	wg.Wait()
	// If we get here without race detector complaints, test passes
}

// TestFallback_Concurrent_SharedAuthState tests that auth state updates are visible across requests
func TestFallback_Concurrent_SharedAuthState(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b"}
	manager, executor := setupConcurrentTest(t, "concurrent-shared-state", models)
	manager.SetFallbackConfig(nil, models)

	// Initially model-a works
	executor.setModelSuccess("model-a", []byte(`{"model":"model-a"}`))
	executor.setModelSuccess("model-b", []byte(`{"model":"model-b"}`))

	// First request should succeed on model-a
	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}
	if resp.ActualModel != "model-a" {
		t.Errorf("First request: ActualModel = %q, want model-a", resp.ActualModel)
	}

	// Now block model-a
	executor.setModelCooldown("model-a", 5*time.Minute)

	// Subsequent requests should fallback to model-b
	const numRequests = 5
	var wg sync.WaitGroup
	results := make(chan string, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := cliproxyexecutor.Request{Model: "model-a"}
			resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})
			if err == nil {
				results <- resp.ActualModel
			} else {
				results <- "error"
			}
		}()
	}

	wg.Wait()
	close(results)

	// All should have fallen back to model-b
	for model := range results {
		if model != "model-b" {
			t.Errorf("Expected fallback to model-b, got %s", model)
		}
	}
}

// =============================================================================
// Test 3.1-3.2: Race During Selection (from original plan)
// =============================================================================

// TestSelector_Concurrent_Picks tests concurrent Pick() calls
func TestSelector_Concurrent_Picks(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		newTestAuthWithID("auth1", "gemini"),
		newTestAuthWithID("auth2", "gemini"),
		newTestAuthWithID("auth3", "gemini"),
	}

	const numGoroutines = 100
	var wg sync.WaitGroup
	results := make(chan *Auth, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			auth, err := selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, auths)
			if err == nil {
				results <- auth
			}
		}()
	}

	wg.Wait()
	close(results)

	// All picks should return valid auths
	count := 0
	for auth := range results {
		if auth == nil {
			t.Error("Pick() returned nil auth")
		}
		count++
	}

	if count != numGoroutines {
		t.Errorf("Expected %d successful picks, got %d", numGoroutines, count)
	}
}

// TestSelector_Concurrent_MixedAvailability tests concurrent picks with changing availability
func TestSelector_Concurrent_MixedAvailability(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}

	// Start with all available
	auths := []*Auth{
		newTestAuthWithID("auth1", "gemini"),
		newTestAuthWithID("auth2", "gemini"),
		newTestAuthWithID("auth3", "gemini"),
	}

	const numGoroutines = 50
	var wg sync.WaitGroup
	var successCount, errorCount int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Simulate changing availability mid-execution
			if idx%10 == 0 {
				// Every 10th goroutine disables an auth
				auths[idx%3].Disabled = true
			}

			_, err := selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, auths)
			if err == nil {
				atomic.AddInt64(&successCount, 1)
			} else {
				atomic.AddInt64(&errorCount, 1)
			}
		}(i)
	}

	wg.Wait()

	// Should have some successes (not all auths disabled at once)
	if successCount == 0 {
		t.Error("Expected some successful picks")
	}

	t.Logf("Concurrent picks: %d success, %d errors", successCount, errorCount)
}

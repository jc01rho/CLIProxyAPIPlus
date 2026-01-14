// Package auth provides authentication selection and management.
// This file contains comprehensive tests for fallback chain behavior.
// It covers basic fallback execution, model-specific overrides, circular reference detection,
// depth limits, and no-fallback-configured scenarios.
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

// fallbackTestExecutor is a mock executor for fallback chain tests
type fallbackTestExecutor struct {
	identifier   string
	modelResults map[string]fallbackModelResult
	callOrder    []string
	mu           sync.Mutex
}

type fallbackModelResult struct {
	resp cliproxyexecutor.Response
	err  error
}

func newFallbackTestExecutor(provider string) *fallbackTestExecutor {
	return &fallbackTestExecutor{
		identifier:   provider,
		modelResults: make(map[string]fallbackModelResult),
		callOrder:    make([]string, 0),
	}
}

func (e *fallbackTestExecutor) Identifier() string {
	return e.identifier
}

func (e *fallbackTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.callOrder = append(e.callOrder, req.Model)
	result, ok := e.modelResults[req.Model]
	e.mu.Unlock()

	if ok {
		return result.resp, result.err
	}
	// Default: return cooldown error if not configured
	return cliproxyexecutor.Response{}, newModelCooldownError(req.Model, e.identifier, time.Minute)
}

func (e *fallbackTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	e.mu.Lock()
	e.callOrder = append(e.callOrder, req.Model)
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

func (e *fallbackTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *fallbackTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *fallbackTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *fallbackTestExecutor) setModelSuccess(model string, payload []byte) {
	e.mu.Lock()
	e.modelResults[model] = fallbackModelResult{
		resp: cliproxyexecutor.Response{Payload: payload, ActualModel: model},
	}
	e.mu.Unlock()
}

func (e *fallbackTestExecutor) setModelCooldown(model string, duration time.Duration) {
	e.mu.Lock()
	e.modelResults[model] = fallbackModelResult{
		err: newModelCooldownError(model, e.identifier, duration),
	}
	e.mu.Unlock()
}

func (e *fallbackTestExecutor) getCallOrder() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]string, len(e.callOrder))
	copy(result, e.callOrder)
	return result
}

func (e *fallbackTestExecutor) clearCallOrder() {
	e.mu.Lock()
	e.callOrder = make([]string, 0)
	e.mu.Unlock()
}

// setupFallbackTest creates a manager with auth and executor for fallback testing
func setupFallbackTest(t *testing.T, authID string, models []string) (*Manager, *fallbackTestExecutor) {
	t.Helper()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	executor := newFallbackTestExecutor("test-provider")
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
// Test 2.1: Basic Fallback Chain Execution (5 scenarios)
// =============================================================================

func TestFallback_BasicChainExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		fallbackChain []string
		modelSetup    func(e *fallbackTestExecutor)
		requestModel  string
		expectSuccess bool
		expectModel   string
		expectCallSeq []string
	}{
		{
			name:          "SingleFallback_FirstBlockedSecondWorks",
			fallbackChain: []string{"model-a", "model-b"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelSuccess("model-b", []byte(`{"model":"model-b"}`))
			},
			requestModel:  "model-a",
			expectSuccess: true,
			expectModel:   "model-b",
			expectCallSeq: []string{"model-a", "model-b"},
		},
		{
			name:          "ChainOf3_FirstTwoBlockedThirdWorks",
			fallbackChain: []string{"model-a", "model-b", "model-c", "model-d"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
				e.setModelCooldown("model-c", 5*time.Minute)
				e.setModelSuccess("model-d", []byte(`{"model":"model-d"}`))
			},
			requestModel:  "model-a",
			expectSuccess: true,
			expectModel:   "model-d",
			expectCallSeq: []string{"model-a", "model-b", "model-c", "model-d"},
		},
		{
			name:          "AllModelsInCooldown_ReturnsCooldownError",
			fallbackChain: []string{"model-a", "model-b", "model-c"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
				e.setModelCooldown("model-c", 5*time.Minute)
			},
			requestModel:  "model-a",
			expectSuccess: false,
			expectModel:   "",
			expectCallSeq: []string{"model-a", "model-b", "model-c"},
		},
		{
			name:          "FirstModelWorks_NoFallbackNeeded",
			fallbackChain: []string{"model-a", "model-b"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelSuccess("model-a", []byte(`{"model":"model-a"}`))
				e.setModelSuccess("model-b", []byte(`{"model":"model-b"}`))
			},
			requestModel:  "model-a",
			expectSuccess: true,
			expectModel:   "model-a",
			expectCallSeq: []string{"model-a"},
		},
		{
			name:          "MiddleModelWorks_StopsAtMiddle",
			fallbackChain: []string{"model-a", "model-b", "model-c"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelSuccess("model-b", []byte(`{"model":"model-b"}`))
				e.setModelSuccess("model-c", []byte(`{"model":"model-c"}`))
			},
			requestModel:  "model-a",
			expectSuccess: true,
			expectModel:   "model-b",
			expectCallSeq: []string{"model-a", "model-b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, executor := setupFallbackTest(t, "basic-chain-"+tt.name, tt.fallbackChain)
			manager.SetFallbackConfig(nil, tt.fallbackChain)
			tt.modelSetup(executor)

			req := cliproxyexecutor.Request{Model: tt.requestModel}
			resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			callOrder := executor.getCallOrder()

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
				assertModelCooldownError(t, err)
			}

			// Verify call sequence
			if len(callOrder) != len(tt.expectCallSeq) {
				t.Errorf("Call sequence length = %d, want %d. Got: %v", len(callOrder), len(tt.expectCallSeq), callOrder)
			} else {
				for i, expected := range tt.expectCallSeq {
					if callOrder[i] != expected {
						t.Errorf("Call[%d] = %q, want %q", i, callOrder[i], expected)
					}
				}
			}
		})
	}
}

// =============================================================================
// Test 2.2: Fallback Model Specific Override (4 scenarios)
// =============================================================================

func TestFallback_ModelSpecificOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		fallbackModels map[string]string
		fallbackChain  []string
		models         []string
		modelSetup     func(e *fallbackTestExecutor)
		requestModel   string
		expectSuccess  bool
		expectModel    string
		expectCallSeq  []string
	}{
		{
			name:           "ModelSpecificOnly_UsesSpecificFallback",
			fallbackModels: map[string]string{"model-a": "model-x"},
			fallbackChain:  nil,
			models:         []string{"model-a", "model-x"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelSuccess("model-x", []byte(`{"model":"model-x"}`))
			},
			requestModel:  "model-a",
			expectSuccess: true,
			expectModel:   "model-x",
			expectCallSeq: []string{"model-a", "model-x"},
		},
		{
			name:           "ChainPlusSpecific_SpecificWins",
			fallbackModels: map[string]string{"model-a": "model-z"},
			fallbackChain:  []string{"model-a", "model-b"},
			models:         []string{"model-a", "model-b", "model-z"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelSuccess("model-z", []byte(`{"model":"model-z"}`))
				e.setModelSuccess("model-b", []byte(`{"model":"model-b"}`))
			},
			requestModel:  "model-a",
			expectSuccess: true,
			expectModel:   "model-z",
			expectCallSeq: []string{"model-a", "model-z"},
		},
		{
			name:           "SpecificNotMatching_UsesChain",
			fallbackModels: map[string]string{"model-x": "model-y"},
			fallbackChain:  []string{"model-a", "model-b"},
			models:         []string{"model-a", "model-b", "model-x", "model-y"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelSuccess("model-b", []byte(`{"model":"model-b"}`))
			},
			requestModel:  "model-a",
			expectSuccess: true,
			expectModel:   "model-b",
			expectCallSeq: []string{"model-a", "model-b"},
		},
		{
			name:           "ChainedSpecificFallback_FollowsChain",
			fallbackModels: map[string]string{"model-a": "model-b", "model-b": "model-c"},
			fallbackChain:  nil,
			models:         []string{"model-a", "model-b", "model-c"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
				e.setModelSuccess("model-c", []byte(`{"model":"model-c"}`))
			},
			requestModel:  "model-a",
			expectSuccess: true,
			expectModel:   "model-c",
			expectCallSeq: []string{"model-a", "model-b", "model-c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, executor := setupFallbackTest(t, "specific-override-"+tt.name, tt.models)
			manager.SetFallbackConfig(tt.fallbackModels, tt.fallbackChain)
			tt.modelSetup(executor)

			req := cliproxyexecutor.Request{Model: tt.requestModel}
			resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			callOrder := executor.getCallOrder()

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

			// Verify call sequence
			if len(callOrder) != len(tt.expectCallSeq) {
				t.Errorf("Call sequence length = %d, want %d. Got: %v", len(callOrder), len(tt.expectCallSeq), callOrder)
			} else {
				for i, expected := range tt.expectCallSeq {
					if callOrder[i] != expected {
						t.Errorf("Call[%d] = %q, want %q", i, callOrder[i], expected)
					}
				}
			}
		})
	}
}

// =============================================================================
// Test 2.3: Circular Reference Detection (5 scenarios)
// =============================================================================

func TestFallback_CircularReferenceDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		fallbackModels map[string]string
		fallbackChain  []string
		models         []string
		modelSetup     func(e *fallbackTestExecutor)
		requestModel   string
		expectMaxCalls int // Maximum number of calls (should stop before infinite loop)
	}{
		{
			name:           "SelfReference_StopsImmediately",
			fallbackModels: map[string]string{"model-a": "model-a"},
			fallbackChain:  nil,
			models:         []string{"model-a"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
			},
			requestModel:   "model-a",
			expectMaxCalls: 2, // Initial call + 1 fallback attempt (detects visited)
		},
		{
			name:           "TwoStepCycle_StopsAfterBoth",
			fallbackModels: map[string]string{"model-a": "model-b", "model-b": "model-a"},
			fallbackChain:  nil,
			models:         []string{"model-a", "model-b"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
			},
			requestModel:   "model-a",
			expectMaxCalls: 2, // model-a -> model-b -> (skip model-a, already visited)
		},
		{
			name:           "ThreeStepCycle_StopsAfterC",
			fallbackModels: map[string]string{"model-a": "model-b", "model-b": "model-c", "model-c": "model-a"},
			fallbackChain:  nil,
			models:         []string{"model-a", "model-b", "model-c"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
				e.setModelCooldown("model-c", 5*time.Minute)
			},
			requestModel:   "model-a",
			expectMaxCalls: 3, // a -> b -> c -> (skip a, visited)
		},
		{
			name:           "ChainWithDuplicate_SkipsSecondOccurrence",
			fallbackModels: nil,
			fallbackChain:  []string{"model-a", "model-b", "model-c", "model-a", "model-d"},
			models:         []string{"model-a", "model-b", "model-c", "model-d"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
				e.setModelCooldown("model-c", 5*time.Minute)
				e.setModelSuccess("model-d", []byte(`{"model":"model-d"}`))
			},
			requestModel:   "model-a",
			expectMaxCalls: 4, // a -> b -> c -> (skip a) -> d
		},
		{
			name:           "CrossReferenceInChain_NoInfiniteLoop",
			fallbackModels: map[string]string{"model-b": "model-a"},
			fallbackChain:  []string{"model-a", "model-b"},
			models:         []string{"model-a", "model-b"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
			},
			requestModel:   "model-a",
			expectMaxCalls: 2, // a -> b -> (skip a via FallbackModels, visited)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, executor := setupFallbackTest(t, "circular-ref-"+tt.name, tt.models)
			manager.SetFallbackConfig(tt.fallbackModels, tt.fallbackChain)
			tt.modelSetup(executor)

			req := cliproxyexecutor.Request{Model: tt.requestModel}
			_, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			callOrder := executor.getCallOrder()

			// Should have returned an error (all models in cooldown)
			if err == nil {
				t.Fatal("Expected error (all models blocked), got success")
			}

			// Should not exceed max calls (no infinite loop)
			if len(callOrder) > tt.expectMaxCalls {
				t.Errorf("Call count = %d, exceeds max %d. Got: %v (potential infinite loop)", len(callOrder), tt.expectMaxCalls, callOrder)
			}

			// Verify no model appears more than once (visited tracking works)
			seen := make(map[string]int)
			for _, model := range callOrder {
				seen[model]++
				if seen[model] > 1 {
					t.Errorf("Model %q called %d times (visited tracking failed)", model, seen[model])
				}
			}
		})
	}
}

// =============================================================================
// Test 2.4: Depth Limits (Max 20) (4 scenarios)
// =============================================================================

func TestFallback_DepthLimits(t *testing.T) {
	t.Parallel()

	// Helper to generate model names
	genModels := func(count int) []string {
		models := make([]string, count)
		for i := 0; i < count; i++ {
			models[i] = "model-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		}
		return models
	}

	// Helper to generate fallback map (each model points to next)
	genFallbackMap := func(models []string) map[string]string {
		m := make(map[string]string)
		for i := 0; i < len(models)-1; i++ {
			m[models[i]] = models[i+1]
		}
		return m
	}

	tests := []struct {
		name          string
		modelCount    int
		successAtIdx  int // -1 means no success (all blocked)
		expectSuccess bool
		expectMaxCall int
	}{
		{
			name:          "Exactly20Fallbacks_SuccessAtLast",
			modelCount:    21, // 20 fallbacks from model 0 to model 20
			successAtIdx:  20, // Last model works
			expectSuccess: true,
			expectMaxCall: 21,
		},
		{
			name:          "Exceeds20_StopsAtDepth20",
			modelCount:    25,
			successAtIdx:  22, // Model 22 would work, but we stop at depth 20
			expectSuccess: false,
			expectMaxCall: 21, // 20 depth + 1 initial = 21 max
		},
		{
			name:          "EarlySuccess_StopsAtDepth5",
			modelCount:    10,
			successAtIdx:  5, // Model 5 works
			expectSuccess: true,
			expectMaxCall: 6, // 5 fallbacks + 1 initial
		},
		{
			name:          "All20Blocked_ReturnsError",
			modelCount:    20,
			successAtIdx:  -1, // All blocked
			expectSuccess: false,
			expectMaxCall: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			models := genModels(tt.modelCount)
			fallbackMap := genFallbackMap(models)

			manager, executor := setupFallbackTest(t, "depth-limit-"+tt.name, models)
			manager.SetFallbackConfig(fallbackMap, nil)

			// Set all models to cooldown, except successAtIdx
			for i, model := range models {
				if i == tt.successAtIdx {
					executor.setModelSuccess(model, []byte(`{"model":"`+model+`"}`))
				} else {
					executor.setModelCooldown(model, 5*time.Minute)
				}
			}

			req := cliproxyexecutor.Request{Model: models[0]}
			resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			callOrder := executor.getCallOrder()

			if tt.expectSuccess {
				if err != nil {
					t.Fatalf("Expected success, got error: %v", err)
				}
				expectedModel := models[tt.successAtIdx]
				if resp.ActualModel != expectedModel {
					t.Errorf("ActualModel = %q, want %q", resp.ActualModel, expectedModel)
				}
			} else {
				if err == nil {
					t.Fatal("Expected error, got success")
				}
			}

			// Verify call count doesn't exceed max
			if len(callOrder) > tt.expectMaxCall {
				t.Errorf("Call count = %d, exceeds max %d (depth limit violated)", len(callOrder), tt.expectMaxCall)
			}
		})
	}
}

// =============================================================================
// Test 2.5: No Fallback Configured (4 scenarios)
// =============================================================================

func TestFallback_NoFallbackConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		fallbackModels map[string]string
		fallbackChain  []string
		models         []string
		requestModel   string
		expectCallSeq  []string
	}{
		{
			name:           "BothNil_NoFallback",
			fallbackModels: nil,
			fallbackChain:  nil,
			models:         []string{"model-a", "model-b"},
			requestModel:   "model-a",
			expectCallSeq:  []string{"model-a"},
		},
		{
			name:           "EmptyMap_NoFallback",
			fallbackModels: map[string]string{},
			fallbackChain:  nil,
			models:         []string{"model-a", "model-b"},
			requestModel:   "model-a",
			expectCallSeq:  []string{"model-a"},
		},
		{
			name:           "EmptyChain_NoFallback",
			fallbackModels: nil,
			fallbackChain:  []string{},
			models:         []string{"model-a", "model-b"},
			requestModel:   "model-a",
			expectCallSeq:  []string{"model-a"},
		},
		{
			name:           "ModelNotInChain_NoFallback",
			fallbackModels: nil,
			fallbackChain:  []string{"model-x", "model-y"},
			models:         []string{"model-a", "model-x", "model-y"},
			requestModel:   "model-a", // Not in chain
			expectCallSeq:  []string{"model-a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, executor := setupFallbackTest(t, "no-fallback-"+tt.name, tt.models)
			manager.SetFallbackConfig(tt.fallbackModels, tt.fallbackChain)

			// All models in cooldown
			for _, model := range tt.models {
				executor.setModelCooldown(model, 5*time.Minute)
			}

			req := cliproxyexecutor.Request{Model: tt.requestModel}
			_, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			callOrder := executor.getCallOrder()

			// Should return error (no fallback triggered)
			if err == nil {
				t.Fatal("Expected error, got success")
			}
			assertModelCooldownError(t, err)

			// Verify only the requested model was called (no fallback)
			if len(callOrder) != len(tt.expectCallSeq) {
				t.Errorf("Call sequence length = %d, want %d. Got: %v", len(callOrder), len(tt.expectCallSeq), callOrder)
			} else {
				for i, expected := range tt.expectCallSeq {
					if callOrder[i] != expected {
						t.Errorf("Call[%d] = %q, want %q", i, callOrder[i], expected)
					}
				}
			}
		})
	}
}

// =============================================================================
// Test 2.6: Fallback with Streaming (additional scenarios)
// =============================================================================

func TestFallback_StreamExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		fallbackChain []string
		modelSetup    func(e *fallbackTestExecutor)
		requestModel  string
		expectSuccess bool
		expectModel   string
	}{
		{
			name:          "Stream_SingleFallback",
			fallbackChain: []string{"model-a", "model-b"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelSuccess("model-b", []byte(`stream-data`))
			},
			requestModel:  "model-a",
			expectSuccess: true,
			expectModel:   "model-b",
		},
		{
			name:          "Stream_AllBlocked",
			fallbackChain: []string{"model-a", "model-b"},
			modelSetup: func(e *fallbackTestExecutor) {
				e.setModelCooldown("model-a", 5*time.Minute)
				e.setModelCooldown("model-b", 5*time.Minute)
			},
			requestModel:  "model-a",
			expectSuccess: false,
			expectModel:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, executor := setupFallbackTest(t, "stream-fallback-"+tt.name, tt.fallbackChain)
			manager.SetFallbackConfig(nil, tt.fallbackChain)
			tt.modelSetup(executor)

			req := cliproxyexecutor.Request{Model: tt.requestModel}
			chunks, err := manager.ExecuteStream(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			if tt.expectSuccess {
				if err != nil {
					t.Fatalf("Expected success, got error: %v", err)
				}
				if chunks == nil {
					t.Fatal("Expected chunks channel, got nil")
				}
				// Drain the channel
				for range chunks {
				}
			} else {
				if err == nil {
					t.Fatal("Expected error, got success")
				}
			}
		})
	}
}

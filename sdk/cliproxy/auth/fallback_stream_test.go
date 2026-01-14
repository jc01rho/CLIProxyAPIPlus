// Package auth provides authentication selection and management.
// This file contains tests for streaming fallback behavior.
// It verifies that fallback behaves correctly for streaming execution,
// including mid-stream failures and initial stream failures.
package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// streamTestExecutor is a mock executor for streaming fallback tests
type streamTestExecutor struct {
	identifier    string
	streamResults map[string]streamModelResult
	callOrder     []string
	mu            sync.Mutex
}

type streamModelResult struct {
	chunks       []cliproxyexecutor.StreamChunk
	initialErr   error // Error returned immediately (triggers fallback)
	failAtChunk  int   // If > 0, fail after this many chunks (mid-stream failure)
	midStreamErr error // Error to send mid-stream
}

func newStreamTestExecutor(provider string) *streamTestExecutor {
	return &streamTestExecutor{
		identifier:    provider,
		streamResults: make(map[string]streamModelResult),
		callOrder:     make([]string, 0),
	}
}

func (e *streamTestExecutor) Identifier() string {
	return e.identifier
}

func (e *streamTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.callOrder = append(e.callOrder, req.Model)
	result, ok := e.streamResults[req.Model]
	e.mu.Unlock()

	if ok && result.initialErr != nil {
		return cliproxyexecutor.Response{}, result.initialErr
	}
	return cliproxyexecutor.Response{ActualModel: req.Model}, nil
}

func (e *streamTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	e.mu.Lock()
	e.callOrder = append(e.callOrder, req.Model)
	result, ok := e.streamResults[req.Model]
	e.mu.Unlock()

	// If initial error, return it immediately (triggers fallback)
	if ok && result.initialErr != nil {
		return nil, result.initialErr
	}

	ch := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(ch)

		if !ok {
			// Default: send one chunk
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte("default-chunk")}
			return
		}

		for i, chunk := range result.chunks {
			// Check for mid-stream failure
			if result.failAtChunk > 0 && i >= result.failAtChunk {
				if result.midStreamErr != nil {
					ch <- cliproxyexecutor.StreamChunk{Err: result.midStreamErr}
				}
				return
			}

			select {
			case ch <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

func (e *streamTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *streamTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *streamTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *streamTestExecutor) setInitialError(model string, err error) {
	e.mu.Lock()
	e.streamResults[model] = streamModelResult{initialErr: err}
	e.mu.Unlock()
}

func (e *streamTestExecutor) setStreamSuccess(model string, chunks []cliproxyexecutor.StreamChunk) {
	e.mu.Lock()
	e.streamResults[model] = streamModelResult{chunks: chunks}
	e.mu.Unlock()
}

func (e *streamTestExecutor) setMidStreamFailure(model string, chunks []cliproxyexecutor.StreamChunk, failAtChunk int, err error) {
	e.mu.Lock()
	e.streamResults[model] = streamModelResult{
		chunks:       chunks,
		failAtChunk:  failAtChunk,
		midStreamErr: err,
	}
	e.mu.Unlock()
}

func (e *streamTestExecutor) getCallOrder() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]string, len(e.callOrder))
	copy(result, e.callOrder)
	return result
}

// setupStreamTest creates a manager with auth and executor for stream testing
func setupStreamTest(t *testing.T, authID string, models []string) (*Manager, *streamTestExecutor) {
	t.Helper()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	executor := newStreamTestExecutor("test-provider")
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
// Test 2.7: Partial Success / Mid-Stream Failure (4 scenarios)
// =============================================================================

func TestFallback_StreamScenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		fallbackChain       []string
		setupExecutor       func(e *streamTestExecutor)
		requestModel        string
		expectStreamSuccess bool
		expectFallback      bool
		expectChunkCount    int
		description         string
	}{
		{
			name:          "StreamStartsThenFails_NoMidStreamFallback",
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutor: func(e *streamTestExecutor) {
				// model-a: sends 5 chunks, then fails
				chunks := make([]cliproxyexecutor.StreamChunk, 5)
				for i := 0; i < 5; i++ {
					chunks[i] = cliproxyexecutor.StreamChunk{Payload: []byte("chunk-" + string(rune('0'+i)))}
				}
				e.setMidStreamFailure("model-a", chunks, 5, errors.New("mid-stream failure"))
				// model-b: would succeed
				e.setStreamSuccess("model-b", []cliproxyexecutor.StreamChunk{
					{Payload: []byte("fallback-chunk")},
				})
			},
			requestModel:        "model-a",
			expectStreamSuccess: true,  // Stream starts successfully
			expectFallback:      false, // No mid-stream fallback
			expectChunkCount:    5,     // 5 data chunks (error chunk not counted as failAtChunk stops iteration)
			description:         "Current implementation does NOT support mid-stream fallback",
		},
		{
			name:          "InitialStreamFailure_TriggersFallback",
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutor: func(e *streamTestExecutor) {
				// model-a: fails immediately with cooldown error
				e.setInitialError("model-a", newModelCooldownError("model-a", "test-provider", 5*time.Minute))
				// model-b: succeeds
				e.setStreamSuccess("model-b", []cliproxyexecutor.StreamChunk{
					{Payload: []byte("fallback-chunk-1")},
					{Payload: []byte("fallback-chunk-2")},
				})
			},
			requestModel:        "model-a",
			expectStreamSuccess: true,
			expectFallback:      true,
			expectChunkCount:    2, // model-b's chunks
			description:         "Initial failure DOES trigger fallback",
		},
		{
			name:          "StreamChannelClosedEarly_GracefulCompletion",
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutor: func(e *streamTestExecutor) {
				// model-a: sends 3 chunks, then closes (no error)
				chunks := make([]cliproxyexecutor.StreamChunk, 3)
				for i := 0; i < 3; i++ {
					chunks[i] = cliproxyexecutor.StreamChunk{Payload: []byte("chunk-" + string(rune('0'+i)))}
				}
				e.setStreamSuccess("model-a", chunks)
			},
			requestModel:        "model-a",
			expectStreamSuccess: true,
			expectFallback:      false,
			expectChunkCount:    3,
			description:         "Early EOF is graceful completion, not error",
		},
		{
			name:          "ContextCancelledDuringStream_ReturnsContextError",
			fallbackChain: []string{"model-a", "model-b"},
			setupExecutor: func(e *streamTestExecutor) {
				// model-a: sends many chunks slowly
				chunks := make([]cliproxyexecutor.StreamChunk, 100)
				for i := 0; i < 100; i++ {
					chunks[i] = cliproxyexecutor.StreamChunk{Payload: []byte("chunk")}
				}
				e.setStreamSuccess("model-a", chunks)
			},
			requestModel:        "model-a",
			expectStreamSuccess: true,  // Stream starts
			expectFallback:      false, // No fallback on context cancel
			expectChunkCount:    -1,    // Variable (depends on when cancel happens)
			description:         "Context cancellation returns context error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, executor := setupStreamTest(t, "stream-test-"+tt.name, tt.fallbackChain)
			manager.SetFallbackConfig(nil, tt.fallbackChain)
			tt.setupExecutor(executor)

			// For context cancellation test, create a cancellable context
			ctx := context.Background()
			var cancel context.CancelFunc
			if tt.name == "ContextCancelledDuringStream_ReturnsContextError" {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
			}

			req := cliproxyexecutor.Request{Model: tt.requestModel}
			chunks, err := manager.ExecuteStream(ctx, []string{"test-provider"}, req, cliproxyexecutor.Options{})

			if tt.expectStreamSuccess {
				if err != nil {
					t.Fatalf("Expected stream to start successfully, got error: %v", err)
				}
				if chunks == nil {
					t.Fatal("Expected chunks channel, got nil")
				}

				// Cancel context for that specific test after first chunk
				chunkCount := 0
				for chunk := range chunks {
					chunkCount++
					_ = chunk

					// For context cancellation test, cancel after first chunk
					if tt.name == "ContextCancelledDuringStream_ReturnsContextError" && chunkCount == 1 {
						cancel()
					}
				}

				// Verify chunk count (if expected)
				if tt.expectChunkCount >= 0 && chunkCount != tt.expectChunkCount {
					t.Errorf("Chunk count = %d, want %d", chunkCount, tt.expectChunkCount)
				}
			} else {
				if err == nil {
					t.Fatal("Expected stream to fail, got success")
				}
			}

			// Verify fallback behavior
			callOrder := executor.getCallOrder()
			if tt.expectFallback {
				if len(callOrder) < 2 {
					t.Errorf("Expected fallback (multiple models called), got: %v", callOrder)
				}
				if len(callOrder) >= 2 && callOrder[1] != "model-b" {
					t.Errorf("Expected fallback to model-b, got: %v", callOrder)
				}
			} else {
				// No fallback - should only call original model (or possibly try providers)
				for _, model := range callOrder {
					if model == "model-b" {
						t.Errorf("Did not expect fallback to model-b, but got: %v", callOrder)
						break
					}
				}
			}
		})
	}
}

// TestFallback_Stream_AllBlockedReturnsCooldown verifies stream returns cooldown error when all blocked
func TestFallback_Stream_AllBlockedReturnsCooldown(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b", "model-c"}
	manager, executor := setupStreamTest(t, "stream-all-blocked", models)
	manager.SetFallbackConfig(nil, models)

	// All models fail with cooldown
	for _, model := range models {
		executor.setInitialError(model, newModelCooldownError(model, "test-provider", 5*time.Minute))
	}

	req := cliproxyexecutor.Request{Model: "model-a"}
	_, err := manager.ExecuteStream(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err == nil {
		t.Fatal("Expected cooldown error, got success")
	}

	assertModelCooldownError(t, err)

	// Verify all models were tried
	callOrder := executor.getCallOrder()
	if len(callOrder) != len(models) {
		t.Errorf("Expected %d models tried, got %d: %v", len(models), len(callOrder), callOrder)
	}
}

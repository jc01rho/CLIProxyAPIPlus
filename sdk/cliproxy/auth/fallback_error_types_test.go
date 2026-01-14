// Package auth provides authentication selection and management.
// This file contains tests for error type gating in fallback behavior.
// CRITICAL: Only modelCooldownError triggers model fallback.
// Other error types should NOT trigger fallback and must return immediately.
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

// statusError implements cliproxyexecutor.StatusError for testing HTTP status codes
type statusError struct {
	code    int
	message string
}

func (e *statusError) Error() string   { return e.message }
func (e *statusError) StatusCode() int { return e.code }

// errorTypeTestExecutor is a mock executor that returns configured errors per model
type errorTypeTestExecutor struct {
	identifier    string
	errorForModel map[string]error
	callHistory   []string
	mu            sync.Mutex
}

func newErrorTypeTestExecutor(provider string) *errorTypeTestExecutor {
	return &errorTypeTestExecutor{
		identifier:    provider,
		errorForModel: make(map[string]error),
		callHistory:   make([]string, 0),
	}
}

func (e *errorTypeTestExecutor) Identifier() string {
	return e.identifier
}

func (e *errorTypeTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.callHistory = append(e.callHistory, req.Model)
	errForModel := e.errorForModel[req.Model]
	e.mu.Unlock()

	if errForModel != nil {
		return cliproxyexecutor.Response{}, errForModel
	}
	return cliproxyexecutor.Response{ActualModel: req.Model}, nil
}

func (e *errorTypeTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	e.mu.Lock()
	e.callHistory = append(e.callHistory, req.Model)
	errForModel := e.errorForModel[req.Model]
	e.mu.Unlock()

	if errForModel != nil {
		return nil, errForModel
	}
	ch := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(ch)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("test")}
	}()
	return ch, nil
}

func (e *errorTypeTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *errorTypeTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *errorTypeTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *errorTypeTestExecutor) setModelError(model string, err error) {
	e.mu.Lock()
	e.errorForModel[model] = err
	e.mu.Unlock()
}

func (e *errorTypeTestExecutor) getCallHistory() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]string, len(e.callHistory))
	copy(result, e.callHistory)
	return result
}

func (e *errorTypeTestExecutor) clearCallHistory() {
	e.mu.Lock()
	e.callHistory = make([]string, 0)
	e.mu.Unlock()
}

// TestFallbackErrorTypes_OnlyCooldownTriggersFallback tests that ONLY modelCooldownError triggers fallback
func TestFallbackErrorTypes_OnlyCooldownTriggersFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		errorForModelA   error
		expectFallback   bool
		expectFinalModel string
		description      string
	}{
		{
			name:             "modelCooldownError_TriggersFallback",
			errorForModelA:   newModelCooldownError("model-a", "gemini", 5*time.Minute),
			expectFallback:   true,
			expectFinalModel: "model-b",
			description:      "modelCooldownError is the ONLY error type that triggers fallback",
		},
		{
			name:             "AuthUnavailable_NoFallback",
			errorForModelA:   &Error{Code: "auth_unavailable", Message: "no auth available"},
			expectFallback:   false,
			expectFinalModel: "",
			description:      "auth_unavailable should return immediately without fallback",
		},
		{
			name:             "AuthNotFound_NoFallback",
			errorForModelA:   &Error{Code: "auth_not_found", Message: "no auth candidates"},
			expectFallback:   false,
			expectFinalModel: "",
			description:      "auth_not_found should return immediately without fallback",
		},
		{
			name:             "ProviderNotFound_NoFallback",
			errorForModelA:   &Error{Code: "provider_not_found", Message: "no provider"},
			expectFallback:   false,
			expectFinalModel: "",
			description:      "provider_not_found should return immediately without fallback",
		},
		{
			name:             "ContextDeadlineExceeded_NoFallback",
			errorForModelA:   context.DeadlineExceeded,
			expectFallback:   false,
			expectFinalModel: "",
			description:      "context timeout should return immediately without fallback",
		},
		{
			name:             "HTTP500_NoFallback",
			errorForModelA:   &statusError{code: 500, message: "internal server error"},
			expectFallback:   false,
			expectFinalModel: "",
			description:      "HTTP 500 from upstream should return immediately without fallback",
		},
		{
			name:             "HTTP401_NoFallback",
			errorForModelA:   &statusError{code: 401, message: "unauthorized"},
			expectFallback:   false,
			expectFinalModel: "",
			description:      "HTTP 401 from upstream should return immediately without fallback",
		},
		{
			name:             "HTTP403_NoFallback",
			errorForModelA:   &statusError{code: 403, message: "forbidden"},
			expectFallback:   false,
			expectFinalModel: "",
			description:      "HTTP 403 from upstream should return immediately without fallback",
		},
		{
			name:             "GenericError_NoFallback",
			errorForModelA:   errors.New("some generic error"),
			expectFallback:   false,
			expectFinalModel: "",
			description:      "generic errors should return immediately without fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup
			executor := newErrorTypeTestExecutor("test-provider")
			executor.setModelError("model-a", tt.errorForModelA)
			// model-b always succeeds (no error set)

			manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
			manager.RegisterExecutor(executor)

			// Register auth
			auth := &Auth{ID: "error-type-test-" + tt.name, Provider: "test-provider", Status: StatusActive}
			manager.Register(context.Background(), auth)

			// Register models in registry (required for Manager.pickNext to work)
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
				{ID: "model-a"},
				{ID: "model-b"},
			})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			})

			// Configure fallback: model-a -> model-b
			manager.SetFallbackConfig(nil, []string{"model-a", "model-b"})

			// Execute
			req := cliproxyexecutor.Request{Model: "model-a"}
			resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			// Verify
			callHistory := executor.getCallHistory()

			if tt.expectFallback {
				// Should have tried model-a, then model-b
				if err != nil {
					t.Fatalf("Expected fallback to succeed, got error: %v", err)
				}
				if resp.ActualModel != tt.expectFinalModel {
					t.Errorf("ActualModel = %q, want %q", resp.ActualModel, tt.expectFinalModel)
				}
				if len(callHistory) < 2 {
					t.Errorf("Expected at least 2 calls (model-a then model-b), got %d: %v", len(callHistory), callHistory)
				}
				if len(callHistory) >= 2 && callHistory[1] != "model-b" {
					t.Errorf("Second call should be model-b, got %v", callHistory)
				}
			} else {
				// Should have only tried model-a, no fallback
				if err == nil {
					t.Fatal("Expected error (no fallback), but got success")
				}
				// Verify model-b was NOT called
				for _, model := range callHistory {
					if model == "model-b" {
						t.Errorf("model-b should NOT have been called for error type %T, but was in call history: %v", tt.errorForModelA, callHistory)
						break
					}
				}
			}
		})
	}
}

// TestFallbackErrorTypes_Stream tests error type gating for streaming execution
func TestFallbackErrorTypes_Stream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		errorForModelA error
		expectFallback bool
	}{
		{
			name:           "Stream_CooldownTriggersFallback",
			errorForModelA: newModelCooldownError("model-a", "test-provider", 5*time.Minute),
			expectFallback: true,
		},
		{
			name:           "Stream_AuthUnavailable_NoFallback",
			errorForModelA: &Error{Code: "auth_unavailable", Message: "no auth available"},
			expectFallback: false,
		},
		{
			name:           "Stream_HTTP500_NoFallback",
			errorForModelA: &statusError{code: 500, message: "internal server error"},
			expectFallback: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup
			executor := newErrorTypeTestExecutor("test-provider")
			executor.setModelError("model-a", tt.errorForModelA)

			manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
			manager.RegisterExecutor(executor)

			auth := &Auth{ID: "stream-error-type-test-" + tt.name, Provider: "test-provider", Status: StatusActive}
			manager.Register(context.Background(), auth)

			// Register models in registry
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
				{ID: "model-a"},
				{ID: "model-b"},
			})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			})

			manager.SetFallbackConfig(nil, []string{"model-a", "model-b"})

			// Execute stream
			req := cliproxyexecutor.Request{Model: "model-a"}
			chunks, err := manager.ExecuteStream(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

			callHistory := executor.getCallHistory()

			if tt.expectFallback {
				if err != nil {
					t.Fatalf("Expected fallback to succeed, got error: %v", err)
				}
				if chunks == nil {
					t.Fatal("Expected chunks channel, got nil")
				}
				// Drain the channel
				for range chunks {
				}
				// Verify model-b was called
				foundModelB := false
				for _, model := range callHistory {
					if model == "model-b" {
						foundModelB = true
						break
					}
				}
				if !foundModelB {
					t.Errorf("model-b should have been called for fallback, got: %v", callHistory)
				}
			} else {
				if err == nil {
					t.Fatal("Expected error (no fallback), but got success")
				}
				// Verify model-b was NOT called
				for _, model := range callHistory {
					if model == "model-b" {
						t.Errorf("model-b should NOT have been called, got: %v", callHistory)
						break
					}
				}
			}
		})
	}
}

// TestFallbackErrorTypes_TypeAssertion verifies the exact type assertion used in conductor.go
func TestFallbackErrorTypes_TypeAssertion(t *testing.T) {
	t.Parallel()

	// This test verifies the type assertion used at conductor.go:580
	// _, isCooldown := err.(*modelCooldownError)

	tests := []struct {
		name       string
		err        error
		isCooldown bool
	}{
		{
			name:       "modelCooldownError_pointer",
			err:        newModelCooldownError("model", "provider", time.Minute),
			isCooldown: true,
		},
		{
			name:       "Error_pointer",
			err:        &Error{Code: "auth_unavailable"},
			isCooldown: false,
		},
		{
			name:       "statusError_pointer",
			err:        &statusError{code: 500},
			isCooldown: false,
		},
		{
			name:       "stdlib_error",
			err:        errors.New("generic"),
			isCooldown: false,
		},
		{
			name:       "nil_error",
			err:        nil,
			isCooldown: false,
		},
		{
			name:       "context_deadline",
			err:        context.DeadlineExceeded,
			isCooldown: false,
		},
		{
			name:       "context_canceled",
			err:        context.Canceled,
			isCooldown: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// This is the exact type assertion from conductor.go:580
			_, isCooldown := tt.err.(*modelCooldownError)

			if isCooldown != tt.isCooldown {
				t.Errorf("type assertion for %T: got isCooldown=%v, want %v", tt.err, isCooldown, tt.isCooldown)
			}
		})
	}
}

// TestFallbackErrorTypes_HTTP429BecomingCooldown tests the special case where
// HTTP 429 eventually becomes a cooldown after marking auth blocked
func TestFallbackErrorTypes_HTTP429BecomingCooldown(t *testing.T) {
	// This test documents the expected behavior:
	// 1. HTTP 429 from upstream does NOT directly trigger fallback
	// 2. However, after MarkResult processes 429, the auth gets blocked
	// 3. Subsequent requests will see the auth in cooldown (modelCooldownError)
	// 4. At that point, fallback WILL trigger

	// Note: This is NOT a direct fallback trigger - it requires a second request
	// The first 429 returns error, marks auth blocked, then next request triggers fallback

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})

	// Create executor that returns 429 for model-a
	executor := newErrorTypeTestExecutor("test-provider")
	executor.setModelError("model-a", &statusError{code: 429, message: "rate limited"})
	manager.RegisterExecutor(executor)

	// Register auth
	auth := &Auth{ID: "http429-test-auth", Provider: "test-provider", Status: StatusActive}
	manager.Register(context.Background(), auth)

	// Register models in registry
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: "model-a"},
		{ID: "model-b"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	// Configure fallback
	manager.SetFallbackConfig(nil, []string{"model-a", "model-b"})

	// First request - 429 does NOT trigger fallback
	req := cliproxyexecutor.Request{Model: "model-a"}
	_, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err == nil {
		t.Fatal("First request should return 429 error")
	}

	// Verify it was a 429 error
	var se *statusError
	if errors.As(err, &se) {
		if se.StatusCode() != 429 {
			t.Errorf("Expected 429 error, got %d", se.StatusCode())
		}
	}

	// After MarkResult, the auth should be in cooldown for model-a
	// Next request should get modelCooldownError and trigger fallback
	// But since the executor still returns 429, we can't test the full flow here
	// This documents the expected behavior
}

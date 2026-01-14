// Package auth provides authentication selection and management.
// This file contains tests for "auth unavailable" error scenarios in selectors.
// It covers cases where no auth is available due to various blocking conditions.
package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// Test helpers for auth construction

func newTestAuthWithID(id, provider string) *Auth {
	return &Auth{
		ID:       id,
		Provider: provider,
		Status:   StatusActive,
	}
}

func setAuthDisabled(auth *Auth) *Auth {
	auth.Disabled = true
	return auth
}

func setAuthStatusDisabled(auth *Auth) *Auth {
	auth.Status = StatusDisabled
	return auth
}

func setAuthCooldown(auth *Auth, duration time.Duration) *Auth {
	auth.Unavailable = true
	auth.NextRetryAfter = time.Now().Add(duration)
	auth.ModelStates = map[string]*ModelState{
		"test-model": {
			Unavailable:    true,
			NextRetryAfter: time.Now().Add(duration),
			Status:         StatusError,
			Quota:          QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(duration)},
		},
	}
	return auth
}

func setModelCooldown(auth *Auth, model string, duration time.Duration) *Auth {
	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*ModelState)
	}
	auth.ModelStates[model] = &ModelState{
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(duration),
		Status:         StatusError,
		Quota:          QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(duration)},
	}
	return auth
}

func setAuthUnavailableWithBlock(auth *Auth, model string, duration time.Duration) *Auth {
	auth.Unavailable = true
	auth.NextRetryAfter = time.Now().Add(duration)
	// Also set model-specific state since isAuthBlockedForModel checks model state first
	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*ModelState)
	}
	auth.ModelStates[model] = &ModelState{
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(duration),
		Status:         StatusError,
	}
	return auth
}

// Assertion helpers

func assertErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var authErr *Error
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if authErr.Code != code {
		t.Errorf("error code = %q, want %q", authErr.Code, code)
	}
}

func assertAuthUnavailableError(t *testing.T, err error) {
	t.Helper()
	assertErrorCode(t, err, "auth_unavailable")
}

func assertAuthNotFoundError(t *testing.T, err error) {
	t.Helper()
	assertErrorCode(t, err, "auth_not_found")
}

func assertModelCooldownError(t *testing.T, err error) {
	t.Helper()
	var cooldownErr *modelCooldownError
	if !errors.As(err, &cooldownErr) {
		t.Fatalf("expected *modelCooldownError, got %T: %v", err, err)
	}
}

// Test 1.1: Empty Auth Candidates
func TestSelector_EmptyAuthCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		selector Selector
	}{
		{"RoundRobinSelector", &RoundRobinSelector{}},
		{"FillFirstSelector", &FillFirstSelector{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auths := []*Auth{} // Empty slice

			_, err := tt.selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, auths)
			if err == nil {
				t.Fatal("Pick() should return error for empty auth list")
			}

			assertAuthNotFoundError(t, err)

			// Verify error message
			var authErr *Error
			if errors.As(err, &authErr) {
				if authErr.Message != "no auth candidates" {
					t.Errorf("error message = %q, want %q", authErr.Message, "no auth candidates")
				}
			}
		})
	}
}

// Test 1.2: All Auths Disabled
func TestSelector_AllAuthsDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		selector Selector
		auths    []*Auth
	}{
		{
			name:     "RoundRobin_AllDisabled",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setAuthDisabled(newTestAuthWithID("auth1", "gemini")),
				setAuthDisabled(newTestAuthWithID("auth2", "gemini")),
				setAuthDisabled(newTestAuthWithID("auth3", "gemini")),
			},
		},
		{
			name:     "FillFirst_AllDisabled",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				setAuthDisabled(newTestAuthWithID("auth1", "gemini")),
				setAuthDisabled(newTestAuthWithID("auth2", "gemini")),
				setAuthDisabled(newTestAuthWithID("auth3", "gemini")),
			},
		},
		{
			name:     "RoundRobin_MixDisabledAndUnavailable",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setAuthDisabled(newTestAuthWithID("auth1", "gemini")),
				setAuthDisabled(newTestAuthWithID("auth2", "gemini")),
				setAuthUnavailableWithBlock(newTestAuthWithID("auth3", "gemini"), "test-model", 30*time.Minute),
				setAuthUnavailableWithBlock(newTestAuthWithID("auth4", "gemini"), "test-model", 30*time.Minute),
				setAuthUnavailableWithBlock(newTestAuthWithID("auth5", "gemini"), "test-model", 30*time.Minute),
			},
		},
		{
			name:     "FillFirst_MixDisabledAndUnavailable",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				setAuthDisabled(newTestAuthWithID("auth1", "gemini")),
				setAuthDisabled(newTestAuthWithID("auth2", "gemini")),
				setAuthUnavailableWithBlock(newTestAuthWithID("auth3", "gemini"), "test-model", 30*time.Minute),
				setAuthUnavailableWithBlock(newTestAuthWithID("auth4", "gemini"), "test-model", 30*time.Minute),
				setAuthUnavailableWithBlock(newTestAuthWithID("auth5", "gemini"), "test-model", 30*time.Minute),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tt.selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, tt.auths)
			if err == nil {
				t.Fatal("Pick() should return error when all auths are disabled")
			}

			assertAuthUnavailableError(t, err)
		})
	}
}

// Test 1.3: All Auths Status Disabled
func TestSelector_AllAuthsStatusDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		selector    Selector
		auths       []*Auth
		expectError bool
	}{
		{
			name:     "RoundRobin_AllStatusDisabled",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setAuthStatusDisabled(newTestAuthWithID("auth1", "gemini")),
				setAuthStatusDisabled(newTestAuthWithID("auth2", "gemini")),
				setAuthStatusDisabled(newTestAuthWithID("auth3", "gemini")),
			},
			expectError: true,
		},
		{
			name:     "FillFirst_AllStatusDisabled",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				setAuthStatusDisabled(newTestAuthWithID("auth1", "gemini")),
				setAuthStatusDisabled(newTestAuthWithID("auth2", "gemini")),
				setAuthStatusDisabled(newTestAuthWithID("auth3", "gemini")),
			},
			expectError: true,
		},
		{
			name:     "RoundRobin_PartialStatusDisabled_ReturnsAvailable",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				newTestAuthWithID("auth1", "gemini"),
				newTestAuthWithID("auth2", "gemini"),
				setAuthStatusDisabled(newTestAuthWithID("auth3", "gemini")),
			},
			expectError: false,
		},
		{
			name:     "FillFirst_PartialStatusDisabled_ReturnsAvailable",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				newTestAuthWithID("auth1", "gemini"),
				newTestAuthWithID("auth2", "gemini"),
				setAuthStatusDisabled(newTestAuthWithID("auth3", "gemini")),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, tt.auths)

			if tt.expectError {
				if err == nil {
					t.Fatal("Pick() should return error when all auths have StatusDisabled")
				}
				assertAuthUnavailableError(t, err)
			} else {
				if err != nil {
					t.Fatalf("Pick() unexpected error: %v", err)
				}
				if got == nil {
					t.Fatal("Pick() returned nil auth")
				}
				// Should return one of the available auths
				if got.Status == StatusDisabled {
					t.Error("Pick() returned a disabled auth")
				}
			}
		})
	}
}

// Test 1.4: All Auths Blocked by NextRetryAfter (Cooldown)
func TestSelector_AllAuthsBlockedByCooldown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		selector       Selector
		auths          []*Auth
		expectCooldown bool
	}{
		{
			name:     "RoundRobin_AllInCooldown",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "test-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth2", "gemini"), "test-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth3", "gemini"), "test-model", 30*time.Minute),
			},
			expectCooldown: true,
		},
		{
			name:     "FillFirst_AllInCooldown",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "test-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth2", "gemini"), "test-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth3", "gemini"), "test-model", 30*time.Minute),
			},
			expectCooldown: true,
		},
		{
			name:     "RoundRobin_PartialCooldown_ReturnsAvailable",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "test-model", 30*time.Minute),
				newTestAuthWithID("auth2", "gemini"),
				newTestAuthWithID("auth3", "gemini"),
			},
			expectCooldown: false,
		},
		{
			name:     "FillFirst_PartialCooldown_ReturnsAvailable",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "test-model", 30*time.Minute),
				newTestAuthWithID("auth2", "gemini"),
				newTestAuthWithID("auth3", "gemini"),
			},
			expectCooldown: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, tt.auths)

			if tt.expectCooldown {
				if err == nil {
					t.Fatal("Pick() should return cooldown error when all auths are in cooldown")
				}
				assertModelCooldownError(t, err)

				// Verify cooldown error has correct model
				var cooldownErr *modelCooldownError
				if errors.As(err, &cooldownErr) {
					if cooldownErr.model != "test-model" {
						t.Errorf("cooldown model = %q, want %q", cooldownErr.model, "test-model")
					}
				}
			} else {
				if err != nil {
					t.Fatalf("Pick() unexpected error: %v", err)
				}
				if got == nil {
					t.Fatal("Pick() returned nil auth")
				}
			}
		})
	}
}

// Test 1.5: Model-Specific Blocking
func TestSelector_ModelSpecificBlocking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		selector    Selector
		auths       []*Auth
		model       string
		expectError bool
	}{
		{
			name:     "RoundRobin_AllBlockedForModel",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "blocked-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth2", "gemini"), "blocked-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth3", "gemini"), "blocked-model", 30*time.Minute),
			},
			model:       "blocked-model",
			expectError: true,
		},
		{
			name:     "FillFirst_AllBlockedForModel",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "blocked-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth2", "gemini"), "blocked-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth3", "gemini"), "blocked-model", 30*time.Minute),
			},
			model:       "blocked-model",
			expectError: true,
		},
		{
			name:     "RoundRobin_PartialModelBlocking_ReturnsUnblocked",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "blocked-model", 30*time.Minute),
				newTestAuthWithID("auth2", "gemini"), // Not blocked for this model
				newTestAuthWithID("auth3", "gemini"), // Not blocked for this model
			},
			model:       "blocked-model",
			expectError: false,
		},
		{
			name:     "FillFirst_PartialModelBlocking_ReturnsUnblocked",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "blocked-model", 30*time.Minute),
				newTestAuthWithID("auth2", "gemini"), // Not blocked for this model
				newTestAuthWithID("auth3", "gemini"), // Not blocked for this model
			},
			model:       "blocked-model",
			expectError: false,
		},
		{
			name:     "RoundRobin_BlockedForDifferentModel_Available",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "other-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth2", "gemini"), "other-model", 30*time.Minute),
			},
			model:       "requested-model", // Different from blocked model
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.selector.Pick(context.Background(), "gemini", tt.model, cliproxyexecutor.Options{}, tt.auths)

			if tt.expectError {
				if err == nil {
					t.Fatal("Pick() should return error when all auths are blocked for model")
				}
				// Could be cooldown or unavailable depending on blocking reason
				var cooldownErr *modelCooldownError
				var authErr *Error
				if !errors.As(err, &cooldownErr) && !errors.As(err, &authErr) {
					t.Fatalf("unexpected error type: %T: %v", err, err)
				}
			} else {
				if err != nil {
					t.Fatalf("Pick() unexpected error: %v", err)
				}
				if got == nil {
					t.Fatal("Pick() returned nil auth")
				}
			}
		})
	}
}

// Test 1.6: Mixed Blocking Scenarios
func TestSelector_MixedBlockingScenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		selector Selector
		auths    []*Auth
	}{
		{
			name:     "RoundRobin_DisabledAndBlocked",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setAuthDisabled(newTestAuthWithID("auth1", "gemini")),
				setModelCooldown(newTestAuthWithID("auth2", "gemini"), "test-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth3", "gemini"), "test-model", 30*time.Minute),
			},
		},
		{
			name:     "FillFirst_DisabledAndBlocked",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				setAuthDisabled(newTestAuthWithID("auth1", "gemini")),
				setModelCooldown(newTestAuthWithID("auth2", "gemini"), "test-model", 30*time.Minute),
				setModelCooldown(newTestAuthWithID("auth3", "gemini"), "test-model", 30*time.Minute),
			},
		},
		{
			name:     "RoundRobin_CooldownDisabledUnavailable",
			selector: &RoundRobinSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "test-model", 30*time.Minute),
				setAuthDisabled(newTestAuthWithID("auth2", "gemini")),
				setAuthStatusDisabled(newTestAuthWithID("auth3", "gemini")),
			},
		},
		{
			name:     "FillFirst_CooldownDisabledUnavailable",
			selector: &FillFirstSelector{},
			auths: []*Auth{
				setModelCooldown(newTestAuthWithID("auth1", "gemini"), "test-model", 30*time.Minute),
				setAuthDisabled(newTestAuthWithID("auth2", "gemini")),
				setAuthStatusDisabled(newTestAuthWithID("auth3", "gemini")),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tt.selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, tt.auths)
			if err == nil {
				t.Fatal("Pick() should return error when all auths are blocked by various reasons")
			}

			// Error could be auth_unavailable or model_cooldown depending on mix
			var cooldownErr *modelCooldownError
			var authErr *Error
			if !errors.As(err, &cooldownErr) && !errors.As(err, &authErr) {
				t.Fatalf("unexpected error type: %T: %v", err, err)
			}
		})
	}
}

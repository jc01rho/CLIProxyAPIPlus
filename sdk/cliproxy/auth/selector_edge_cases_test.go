// Package auth provides authentication selection and management.
// This file contains tests for edge cases in auth selection and fallback.
// It covers zero-length inputs, time-related edge cases, and large scale scenarios.
package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// =============================================================================
// Test 5.1: Zero-Length Inputs
// =============================================================================

func TestSelector_ZeroLengthInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		selector    Selector
		providers   string
		model       string
		auths       []*Auth
		expectError bool
		errorCode   string
	}{
		{
			name:        "EmptyProviders_RoundRobin",
			selector:    &RoundRobinSelector{},
			providers:   "",
			model:       "test-model",
			auths:       []*Auth{newTestAuthWithID("auth1", "gemini")},
			expectError: false, // Provider filter is empty, so all auths match
		},
		{
			name:        "EmptyModel_RoundRobin",
			selector:    &RoundRobinSelector{},
			providers:   "gemini",
			model:       "",
			auths:       []*Auth{newTestAuthWithID("auth1", "gemini")},
			expectError: false, // Empty model is handled gracefully
		},
		{
			name:        "EmptyProviders_FillFirst",
			selector:    &FillFirstSelector{},
			providers:   "",
			model:       "test-model",
			auths:       []*Auth{newTestAuthWithID("auth1", "gemini")},
			expectError: false, // Provider filter is empty, so all auths match
		},
		{
			name:        "EmptyModel_FillFirst",
			selector:    &FillFirstSelector{},
			providers:   "gemini",
			model:       "",
			auths:       []*Auth{newTestAuthWithID("auth1", "gemini")},
			expectError: false, // Empty model is handled gracefully
		},
		{
			name:        "NilAuths_RoundRobin",
			selector:    &RoundRobinSelector{},
			providers:   "gemini",
			model:       "test-model",
			auths:       nil,
			expectError: true,
			errorCode:   "auth_not_found",
		},
		{
			name:        "NilAuths_FillFirst",
			selector:    &FillFirstSelector{},
			providers:   "gemini",
			model:       "test-model",
			auths:       nil,
			expectError: true,
			errorCode:   "auth_not_found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.selector.Pick(context.Background(), tt.providers, tt.model, cliproxyexecutor.Options{}, tt.auths)

			if tt.expectError {
				if err == nil {
					t.Fatal("Pick() should return error")
				}
				if tt.errorCode != "" {
					assertErrorCode(t, err, tt.errorCode)
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

// TestSelector_EmptyOptions tests handling of empty/nil Options
func TestSelector_EmptyOptions(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{newTestAuthWithID("auth1", "gemini")}

	// Empty Options (zero value)
	got, err := selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() with empty Options failed: %v", err)
	}
	if got == nil {
		t.Fatal("Pick() returned nil auth")
	}
}

// =============================================================================
// Test 5.2: Time-Related Edge Cases
// =============================================================================

func TestSelector_TimeEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cooldownSetup func(auth *Auth)
		expectBlocked bool
		description   string
	}{
		{
			name: "CooldownExactlyNow_Available",
			cooldownSetup: func(auth *Auth) {
				// NextRetryAfter = exactly now
				auth.Unavailable = true
				auth.NextRetryAfter = time.Now()
				if auth.ModelStates == nil {
					auth.ModelStates = make(map[string]*ModelState)
				}
				auth.ModelStates["test-model"] = &ModelState{
					Unavailable:    true,
					NextRetryAfter: time.Now(),
				}
			},
			expectBlocked: false, // Cooldown expired at "now"
			description:   "Cooldown that expires exactly now should be available",
		},
		{
			name: "CooldownInPast_Available",
			cooldownSetup: func(auth *Auth) {
				// NextRetryAfter = 1 second ago
				pastTime := time.Now().Add(-time.Second)
				auth.Unavailable = true
				auth.NextRetryAfter = pastTime
				if auth.ModelStates == nil {
					auth.ModelStates = make(map[string]*ModelState)
				}
				auth.ModelStates["test-model"] = &ModelState{
					Unavailable:    true,
					NextRetryAfter: pastTime,
				}
			},
			expectBlocked: false, // Cooldown already expired
			description:   "Cooldown in the past should be available",
		},
		{
			name: "CooldownFarFuture_Blocked",
			cooldownSetup: func(auth *Auth) {
				// NextRetryAfter = 24 hours from now
				futureTime := time.Now().Add(24 * time.Hour)
				auth.Unavailable = true
				auth.NextRetryAfter = futureTime
				if auth.ModelStates == nil {
					auth.ModelStates = make(map[string]*ModelState)
				}
				auth.ModelStates["test-model"] = &ModelState{
					Unavailable:    true,
					NextRetryAfter: futureTime,
					Status:         StatusError,
					Quota:          QuotaState{Exceeded: true, NextRecoverAt: futureTime},
				}
			},
			expectBlocked: true,
			description:   "Cooldown 24 hours in future should be blocked",
		},
		{
			name: "ZeroTime_Available",
			cooldownSetup: func(auth *Auth) {
				// NextRetryAfter = zero time (never set)
				auth.Unavailable = false
				auth.NextRetryAfter = time.Time{}
			},
			expectBlocked: false,
			description:   "Zero time (never set) should be available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			selector := &RoundRobinSelector{}
			auth := newTestAuthWithID("time-test-auth", "gemini")
			tt.cooldownSetup(auth)

			got, err := selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, []*Auth{auth})

			if tt.expectBlocked {
				if err == nil {
					t.Fatal("Pick() should return error for blocked auth")
				}
			} else {
				if err != nil {
					t.Fatalf("Pick() unexpected error for available auth: %v", err)
				}
				if got == nil {
					t.Fatal("Pick() returned nil auth")
				}
			}
		})
	}
}

// =============================================================================
// Test 5.3: Large Numbers
// =============================================================================

func TestSelector_LargeAuthPool(t *testing.T) {
	t.Parallel()

	const numAuths = 1000

	// Create 1000 auths, only the last one is available
	auths := make([]*Auth, numAuths)
	for i := 0; i < numAuths; i++ {
		auths[i] = newTestAuthWithID("auth-"+string(rune('0'+i/100))+string(rune('0'+(i/10)%10))+string(rune('0'+i%10)), "gemini")
		if i < numAuths-1 {
			// All but last are disabled
			auths[i].Disabled = true
		}
	}

	selectors := []struct {
		name     string
		selector Selector
	}{
		{"RoundRobin", &RoundRobinSelector{}},
		{"FillFirst", &FillFirstSelector{}},
	}

	for _, s := range selectors {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()

			start := time.Now()
			got, err := s.selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, auths)
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("Pick() failed: %v", err)
			}
			if got == nil {
				t.Fatal("Pick() returned nil auth")
			}
			if got.Disabled {
				t.Error("Pick() returned disabled auth")
			}

			// Should complete efficiently (under 100ms even with 1000 auths)
			if elapsed > 100*time.Millisecond {
				t.Errorf("Pick() took too long: %v", elapsed)
			}
		})
	}
}

// TestSelector_CursorOverflow tests cursor behavior at boundary values
func TestSelector_CursorOverflow(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}

	// Create a small auth pool
	auths := []*Auth{
		newTestAuthWithID("auth1", "gemini"),
		newTestAuthWithID("auth2", "gemini"),
		newTestAuthWithID("auth3", "gemini"),
	}

	// Make many picks to potentially trigger cursor overflow
	const numPicks = 10000
	for i := 0; i < numPicks; i++ {
		got, err := selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() failed at iteration %d: %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() returned nil at iteration %d", i)
		}
	}
	// If we get here without panic or error, cursor handling is correct
}

// TestSelector_AllAuthsSameProvider tests selection with all auths from same provider
func TestSelector_AllAuthsSameProvider(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}

	// Create multiple auths for same provider
	auths := make([]*Auth, 10)
	for i := 0; i < 10; i++ {
		auths[i] = newTestAuthWithID("same-provider-auth-"+string(rune('0'+i)), "gemini")
	}

	// Pick multiple times and verify round-robin behavior
	picked := make(map[string]int)
	for i := 0; i < 30; i++ {
		got, err := selector.Pick(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() failed: %v", err)
		}
		picked[got.ID]++
	}

	// Each auth should be picked roughly equally (3 times each for 30 picks / 10 auths)
	for id, count := range picked {
		if count < 2 || count > 4 {
			t.Errorf("Auth %s picked %d times, expected ~3", id, count)
		}
	}
}

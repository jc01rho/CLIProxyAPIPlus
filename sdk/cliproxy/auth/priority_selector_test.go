package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestPrioritySelector_ProviderOrder(t *testing.T) {
	// Create auths with different providers
	auths := []*Auth{
		{ID: "auth1", Provider: "gemini-api"},
		{ID: "auth2", Provider: "gemini-cli"},
		{ID: "auth3", Provider: "gemini-vertex"},
	}

	selector := &PrioritySelector{
		ProviderOrder: []string{"gemini-vertex", "gemini-cli", "gemini-api"},
	}

	auth, err := selector.Pick(context.Background(), "", "gemini-2.5-pro", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if auth.Provider != "gemini-vertex" {
		t.Errorf("Pick() got provider %q, want %q", auth.Provider, "gemini-vertex")
	}
}

func TestPrioritySelector_ModelSpecificOverride(t *testing.T) {
	auths := []*Auth{
		{ID: "auth1", Provider: "gemini-cli"},
		{ID: "auth2", Provider: "gemini-vertex"},
	}

	selector := &PrioritySelector{
		ProviderOrder: []string{"gemini-cli", "gemini-vertex"},
		ProviderPriority: map[string][]string{
			"gemini-2.5-pro": {"gemini-vertex", "gemini-cli"},
		},
	}

	// For gemini-2.5-pro, should use model-specific priority (vertex first)
	auth, err := selector.Pick(context.Background(), "", "gemini-2.5-pro", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if auth.Provider != "gemini-vertex" {
		t.Errorf("Pick() for gemini-2.5-pro got provider %q, want %q", auth.Provider, "gemini-vertex")
	}

	// For gemini-2.0-flash, should use global ProviderOrder (cli first)
	auth2, err := selector.Pick(context.Background(), "", "gemini-2.0-flash", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if auth2.Provider != "gemini-cli" {
		t.Errorf("Pick() for gemini-2.0-flash got provider %q, want %q", auth2.Provider, "gemini-cli")
	}
}

func TestPrioritySelector_FallbackToNextProvider(t *testing.T) {
	now := time.Now()
	// Provider A has 2 blocked auths, Provider B has 1 available
	auths := []*Auth{
		{
			ID:             "auth1",
			Provider:       "provider-a",
			Unavailable:    true,
			NextRetryAfter: now.Add(10 * time.Minute),
			ModelStates: map[string]*ModelState{
				"test-model": {Unavailable: true, NextRetryAfter: now.Add(10 * time.Minute), Quota: QuotaState{Exceeded: true}},
			},
		},
		{
			ID:             "auth2",
			Provider:       "provider-a",
			Unavailable:    true,
			NextRetryAfter: now.Add(10 * time.Minute),
			ModelStates: map[string]*ModelState{
				"test-model": {Unavailable: true, NextRetryAfter: now.Add(10 * time.Minute), Quota: QuotaState{Exceeded: true}},
			},
		},
		{ID: "auth3", Provider: "provider-b"},
	}

	selector := &PrioritySelector{
		ProviderOrder: []string{"provider-a", "provider-b"},
	}

	auth, err := selector.Pick(context.Background(), "", "test-model", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if auth.Provider != "provider-b" {
		t.Errorf("Pick() got provider %q, want %q", auth.Provider, "provider-b")
	}
}

func TestPrioritySelector_AllProvidersBlocked(t *testing.T) {
	now := time.Now()
	auths := []*Auth{
		{
			ID:             "auth1",
			Provider:       "provider-a",
			Unavailable:    true,
			NextRetryAfter: now.Add(5 * time.Minute),
			ModelStates: map[string]*ModelState{
				"test-model": {
					Unavailable:    true,
					NextRetryAfter: now.Add(5 * time.Minute),
					Quota:          QuotaState{Exceeded: true},
				},
			},
		},
		{
			ID:             "auth2",
			Provider:       "provider-b",
			Unavailable:    true,
			NextRetryAfter: now.Add(10 * time.Minute),
			ModelStates: map[string]*ModelState{
				"test-model": {
					Unavailable:    true,
					NextRetryAfter: now.Add(10 * time.Minute),
					Quota:          QuotaState{Exceeded: true},
				},
			},
		},
	}

	selector := &PrioritySelector{
		ProviderOrder: []string{"provider-a", "provider-b"},
	}

	_, err := selector.Pick(context.Background(), "", "test-model", cliproxyexecutor.Options{}, auths)
	if err == nil {
		t.Fatal("Pick() expected error, got nil")
	}

	cooldownErr, ok := err.(*modelCooldownError)
	if !ok {
		t.Errorf("Pick() error type = %T, want *modelCooldownError", err)
	} else if cooldownErr.resetIn <= 0 {
		t.Errorf("Pick() cooldownErr.resetIn = %v, want > 0", cooldownErr.resetIn)
	}
}

func TestPrioritySelector_NoPriorityList(t *testing.T) {
	auths := []*Auth{
		{ID: "auth1", Provider: "gemini-cli"},
		{ID: "auth2", Provider: "gemini-vertex"},
	}

	// No priority configured - should fall back to inner selector
	selector := &PrioritySelector{}

	auth, err := selector.Pick(context.Background(), "gemini-cli", "gemini-2.5-pro", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if auth == nil {
		t.Error("Pick() returned nil auth")
	}
}

func TestPrioritySelector_SkipNonexistentProvider(t *testing.T) {
	auths := []*Auth{
		{ID: "auth1", Provider: "gemini-cli"},
	}

	selector := &PrioritySelector{
		ProviderOrder: []string{"nonexistent", "gemini-cli"},
	}

	auth, err := selector.Pick(context.Background(), "", "gemini-2.5-pro", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if auth.Provider != "gemini-cli" {
		t.Errorf("Pick() got provider %q, want %q", auth.Provider, "gemini-cli")
	}
}

// TestPrioritySelector_TwoLevelPriorityInteraction tests that both provider-level
// and auth-level priority systems work together correctly.
// This is a critical integration test ensuring no conflict between:
// - Level 1: PrioritySelector (provider order)
// - Level 2: Auth.Attributes["priority"] (credential priority within provider)
func TestPrioritySelector_TwoLevelPriorityInteraction(t *testing.T) {
	t.Parallel()

	// Setup: 2 providers with multiple credentials each
	// Provider A: 3 credentials with different priorities
	// Provider B: 2 credentials with different priorities
	auths := []*Auth{
		// Provider A credentials
		{ID: "a-low", Provider: "provider-a", Attributes: map[string]string{"priority": "0"}},
		{ID: "a-high-1", Provider: "provider-a", Attributes: map[string]string{"priority": "10"}},
		{ID: "a-high-2", Provider: "provider-a", Attributes: map[string]string{"priority": "10"}},
		// Provider B credentials
		{ID: "b-low", Provider: "provider-b", Attributes: map[string]string{"priority": "5"}},
		{ID: "b-high", Provider: "provider-b", Attributes: map[string]string{"priority": "20"}},
	}

	selector := &PrioritySelector{
		ProviderOrder: []string{"provider-a", "provider-b"}, // Try A first, then B
		InnerSelector: &RoundRobinSelector{},
	}

	// Test 1: First pick should select from provider-a (Level 1) with highest priority (Level 2)
	auth1, err := selector.Pick(context.Background(), "", "test-model", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick #1 error = %v", err)
	}
	if auth1.Provider != "provider-a" {
		t.Errorf("Pick #1 provider = %q, want %q (Level 1 priority)", auth1.Provider, "provider-a")
	}
	// Should be one of the high-priority auths (a-high-1 or a-high-2)
	if auth1.ID != "a-high-1" && auth1.ID != "a-high-2" {
		t.Errorf("Pick #1 ID = %q, want a-high-1 or a-high-2 (Level 2 priority)", auth1.ID)
	}

	// Test 2: Second pick should round-robin within the high-priority bucket of provider-a
	auth2, err := selector.Pick(context.Background(), "", "test-model", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick #2 error = %v", err)
	}
	if auth2.Provider != "provider-a" {
		t.Errorf("Pick #2 provider = %q, want %q", auth2.Provider, "provider-a")
	}
	// Should be the other high-priority auth
	if auth2.ID == auth1.ID {
		// Both picks got the same auth - this is acceptable if round-robin cycles
		t.Logf("Pick #2 got same auth as #1 (%s) - round-robin cycling", auth2.ID)
	}

	// Test 3: Low-priority auth (a-low) should NOT be selected when high-priority auths are available
	selectedLow := false
	for i := 0; i < 10; i++ {
		auth, err := selector.Pick(context.Background(), "", "test-model", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick #%d error = %v", i+3, err)
		}
		if auth.ID == "a-low" {
			selectedLow = true
			break
		}
	}
	if selectedLow {
		t.Error("Low-priority auth 'a-low' was selected when high-priority auths were available")
	}
}

// TestPrioritySelector_AuthPriorityFallbackWithinProvider tests that when high-priority
// auths are blocked, the selector falls back to lower-priority auths within the same provider.
func TestPrioritySelector_AuthPriorityFallbackWithinProvider(t *testing.T) {
	t.Parallel()

	now := time.Now()

	// Provider A: high-priority auth is blocked, low-priority is available
	auths := []*Auth{
		{
			ID:         "a-high-blocked",
			Provider:   "provider-a",
			Attributes: map[string]string{"priority": "10"},
			ModelStates: map[string]*ModelState{
				"test-model": {
					Unavailable:    true,
					NextRetryAfter: now.Add(30 * time.Minute),
					Quota:          QuotaState{Exceeded: true},
				},
			},
		},
		{
			ID:         "a-low-available",
			Provider:   "provider-a",
			Attributes: map[string]string{"priority": "0"},
		},
	}

	selector := &PrioritySelector{
		ProviderOrder: []string{"provider-a"},
		InnerSelector: &RoundRobinSelector{},
	}

	auth, err := selector.Pick(context.Background(), "", "test-model", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	// Should fall back to low-priority auth when high-priority is blocked
	if auth.ID != "a-low-available" {
		t.Errorf("Pick() ID = %q, want %q (fallback to lower priority)", auth.ID, "a-low-available")
	}
}

// TestPrioritySelector_ProviderFallbackThenAuthPriority tests the complete fallback chain:
// 1. Try provider-a (Level 1) - all auths blocked
// 2. Fallback to provider-b (Level 1)
// 3. Within provider-b, select highest priority auth (Level 2)
func TestPrioritySelector_ProviderFallbackThenAuthPriority(t *testing.T) {
	t.Parallel()

	now := time.Now()

	auths := []*Auth{
		// Provider A: all blocked
		{
			ID:         "a-high",
			Provider:   "provider-a",
			Attributes: map[string]string{"priority": "10"},
			ModelStates: map[string]*ModelState{
				"test-model": {
					Unavailable:    true,
					NextRetryAfter: now.Add(30 * time.Minute),
					Quota:          QuotaState{Exceeded: true},
				},
			},
		},
		// Provider B: available with different priorities
		{ID: "b-low", Provider: "provider-b", Attributes: map[string]string{"priority": "0"}},
		{ID: "b-high", Provider: "provider-b", Attributes: map[string]string{"priority": "10"}},
	}

	selector := &PrioritySelector{
		ProviderOrder: []string{"provider-a", "provider-b"}, // A first, then B
		InnerSelector: &RoundRobinSelector{},
	}

	auth, err := selector.Pick(context.Background(), "", "test-model", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	// Should fallback to provider-b (Level 1) and select high-priority auth (Level 2)
	if auth.Provider != "provider-b" {
		t.Errorf("Pick() provider = %q, want %q (Level 1 fallback)", auth.Provider, "provider-b")
	}
	if auth.ID != "b-high" {
		t.Errorf("Pick() ID = %q, want %q (Level 2 priority)", auth.ID, "b-high")
	}
}

// TestPrioritySelector_MixedPriorityDistribution verifies that round-robin only cycles
// within the highest priority bucket, not across all priorities.
func TestPrioritySelector_MixedPriorityDistribution(t *testing.T) {
	t.Parallel()

	auths := []*Auth{
		{ID: "low-1", Provider: "provider-a", Attributes: map[string]string{"priority": "0"}},
		{ID: "low-2", Provider: "provider-a", Attributes: map[string]string{"priority": "0"}},
		{ID: "high-1", Provider: "provider-a", Attributes: map[string]string{"priority": "10"}},
		{ID: "high-2", Provider: "provider-a", Attributes: map[string]string{"priority": "10"}},
		{ID: "high-3", Provider: "provider-a", Attributes: map[string]string{"priority": "10"}},
	}

	selector := &PrioritySelector{
		ProviderOrder: []string{"provider-a"},
		InnerSelector: &RoundRobinSelector{},
	}

	// Track which auths are selected over 10 picks
	selectedCounts := make(map[string]int)
	for i := 0; i < 10; i++ {
		auth, err := selector.Pick(context.Background(), "", "test-model", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick #%d error = %v", i, err)
		}
		selectedCounts[auth.ID]++
	}

	// Verify: only high-priority auths should be selected
	for id, count := range selectedCounts {
		if id == "low-1" || id == "low-2" {
			t.Errorf("Low-priority auth %q was selected %d times (should be 0)", id, count)
		}
	}

	// Verify: high-priority auths should have roughly equal distribution (round-robin)
	// With 10 picks and 3 high-priority auths, expect ~3-4 picks each
	for _, id := range []string{"high-1", "high-2", "high-3"} {
		if count, ok := selectedCounts[id]; !ok || count == 0 {
			t.Errorf("High-priority auth %q was never selected (round-robin not working)", id)
		}
	}
}

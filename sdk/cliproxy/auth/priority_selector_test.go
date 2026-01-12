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

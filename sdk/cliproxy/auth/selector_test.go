package auth

import (
	"context"
	"errors"
	"sync"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestFillFirstSelectorPick_Deterministic(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "a" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "a")
	}
}

func TestFillFirstSelector_ProviderBasedMode(t *testing.T) {
	t.Parallel()

	// In provider-based mode (default), FillFirstSelector always returns first available
	selector := &FillFirstSelector{Mode: "provider-based"}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	// Multiple picks should always return the same (first sorted) auth
	for i := 0; i < 5; i++ {
		got, err := selector.Pick(context.Background(), "gemini", "model1", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got.ID != "a" {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q (always first)", i, got.ID, "a")
		}
	}
}

func TestFillFirstSelector_KeyBasedMode(t *testing.T) {
	t.Parallel()

	// In key-based mode, FillFirstSelector rotates through credentials
	selector := &FillFirstSelector{Mode: "key-based"}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	// Should cycle through all auths (sorted by ID: a, b, c)
	want := []string{"a", "b", "c", "a", "b"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "gemini", "model1", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
	}
}

func TestRoundRobinSelectorPick_CyclesDeterministic(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	want := []string{"a", "b", "c", "a", "b"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
	}
}

func TestRoundRobinSelector_ProviderBasedMode(t *testing.T) {
	t.Parallel()

	// In provider-based mode (default), different providers have separate cursors
	selector := &RoundRobinSelector{Mode: "provider-based"}
	authsA := []*Auth{{ID: "a1"}, {ID: "a2"}}
	authsB := []*Auth{{ID: "b1"}, {ID: "b2"}}

	// Provider A picks
	gotA1, _ := selector.Pick(context.Background(), "providerA", "model1", cliproxyexecutor.Options{}, authsA)
	gotA2, _ := selector.Pick(context.Background(), "providerA", "model1", cliproxyexecutor.Options{}, authsA)

	// Provider B picks (should start from beginning, separate cursor)
	gotB1, _ := selector.Pick(context.Background(), "providerB", "model1", cliproxyexecutor.Options{}, authsB)
	gotB2, _ := selector.Pick(context.Background(), "providerB", "model1", cliproxyexecutor.Options{}, authsB)

	// Provider A should cycle: a1, a2
	if gotA1.ID != "a1" || gotA2.ID != "a2" {
		t.Errorf("Provider A: got %s, %s; want a1, a2", gotA1.ID, gotA2.ID)
	}

	// Provider B should cycle independently: b1, b2
	if gotB1.ID != "b1" || gotB2.ID != "b2" {
		t.Errorf("Provider B: got %s, %s; want b1, b2", gotB1.ID, gotB2.ID)
	}
}

func TestRoundRobinSelector_KeyBasedMode(t *testing.T) {
	t.Parallel()

	// In key-based mode, all providers share the same cursor for the same model
	selector := &RoundRobinSelector{Mode: "key-based"}

	// Combined auths from both providers (simulating what would be passed)
	// In real usage, the caller would combine auths from all providers
	allAuths := []*Auth{
		{ID: "a1"}, // from provider A
		{ID: "a2"}, // from provider A
		{ID: "b1"}, // from provider B
		{ID: "b2"}, // from provider B
	}

	// All picks use the same cursor key (just "model1")
	// Should cycle through all auths regardless of provider
	want := []string{"a1", "a2", "b1", "b2", "a1"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "anyProvider", "model1", cliproxyexecutor.Options{}, allAuths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
	}
}

func TestRoundRobinSelector_KeyBasedMode_CrossProvider(t *testing.T) {
	t.Parallel()

	// Test that key-based mode uses model-only key, so different providers
	// calling with same model share the cursor
	selector := &RoundRobinSelector{Mode: "key-based"}
	auths := []*Auth{{ID: "x"}, {ID: "y"}, {ID: "z"}}

	// Provider A picks first
	got1, _ := selector.Pick(context.Background(), "providerA", "shared-model", cliproxyexecutor.Options{}, auths)
	// Provider B picks next (should continue from same cursor)
	got2, _ := selector.Pick(context.Background(), "providerB", "shared-model", cliproxyexecutor.Options{}, auths)
	// Provider A picks again
	got3, _ := selector.Pick(context.Background(), "providerA", "shared-model", cliproxyexecutor.Options{}, auths)

	// All should cycle through: x, y, z
	if got1.ID != "x" || got2.ID != "y" || got3.ID != "z" {
		t.Errorf("Cross-provider key-based: got %s, %s, %s; want x, y, z", got1.ID, got2.ID, got3.ID)
	}
}

func TestSelectors_ModeIsolation(t *testing.T) {
	t.Parallel()

	// Test that different models have independent cursors
	selector := &RoundRobinSelector{Mode: "key-based"}
	auths := []*Auth{{ID: "a"}, {ID: "b"}, {ID: "c"}}

	// Model 1 picks
	m1_1, _ := selector.Pick(context.Background(), "provider", "model1", cliproxyexecutor.Options{}, auths)
	m1_2, _ := selector.Pick(context.Background(), "provider", "model1", cliproxyexecutor.Options{}, auths)

	// Model 2 picks (should have separate cursor)
	m2_1, _ := selector.Pick(context.Background(), "provider", "model2", cliproxyexecutor.Options{}, auths)
	m2_2, _ := selector.Pick(context.Background(), "provider", "model2", cliproxyexecutor.Options{}, auths)

	// Model 1: a, b
	if m1_1.ID != "a" || m1_2.ID != "b" {
		t.Errorf("Model 1: got %s, %s; want a, b", m1_1.ID, m1_2.ID)
	}

	// Model 2: a, b (independent cursor)
	if m2_1.ID != "a" || m2_2.ID != "b" {
		t.Errorf("Model 2: got %s, %s; want a, b", m2_1.ID, m2_2.ID)
	}
}

func TestRoundRobinSelectorPick_Concurrent(t *testing.T) {
	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	goroutines := 32
	iterations := 100
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if got == nil {
					select {
					case errCh <- errors.New("Pick() returned nil auth"):
					default:
					}
					return
				}
				if got.ID == "" {
					select {
					case errCh <- errors.New("Pick() returned auth with empty ID"):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent Pick() error = %v", err)
	default:
	}
}

func TestFillFirstSelector_KeyBasedMode_Concurrent(t *testing.T) {
	selector := &FillFirstSelector{Mode: "key-based"}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	goroutines := 32
	iterations := 100
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				got, err := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if got == nil {
					select {
					case errCh <- errors.New("Pick() returned nil auth"):
					default:
					}
					return
				}
				if got.ID == "" {
					select {
					case errCh <- errors.New("Pick() returned auth with empty ID"):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent Pick() error = %v", err)
	default:
	}
}

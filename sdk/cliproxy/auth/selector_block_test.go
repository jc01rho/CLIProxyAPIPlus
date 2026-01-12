package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestRoundRobinSelector_BlockedAuthSkipped(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	now := time.Now()

	// Create auths with one blocked
	auths := []*Auth{
		{ID: "auth1", Status: StatusActive, ModelStates: map[string]*ModelState{
			"model1": {
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Status:         StatusError,
			},
		}},
		{ID: "auth2", Status: StatusActive},
		{ID: "auth3", Status: StatusActive},
	}

	// Pick should skip blocked auth1 and return auth2
	got, err := selector.Pick(context.Background(), "gemini", "model1", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "auth2" {
		t.Fatalf("Pick() got %s, want auth2 (auth1 should be blocked)", got.ID)
	}

	// Second pick should return auth3
	got2, err := selector.Pick(context.Background(), "gemini", "model1", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() #2 error = %v", err)
	}
	if got2.ID != "auth3" {
		t.Fatalf("Pick() #2 got %s, want auth3", got2.ID)
	}

	// Third pick should cycle back to auth2 (auth1 still blocked)
	got3, err := selector.Pick(context.Background(), "gemini", "model1", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() #3 error = %v", err)
	}
	if got3.ID != "auth2" {
		t.Fatalf("Pick() #3 got %s, want auth2", got3.ID)
	}
}

func TestRoundRobinSelector_AllBlockedReturnsCooldownError(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	now := time.Now()
	blockTime := now.Add(30 * time.Minute)

	// All auths are blocked with quota exceeded
	auths := []*Auth{
		{ID: "auth1", Status: StatusActive, ModelStates: map[string]*ModelState{
			"model1": {
				Unavailable:    true,
				NextRetryAfter: blockTime,
				Status:         StatusError,
				Quota:          QuotaState{Exceeded: true, NextRecoverAt: blockTime},
			},
		}},
		{ID: "auth2", Status: StatusActive, ModelStates: map[string]*ModelState{
			"model1": {
				Unavailable:    true,
				NextRetryAfter: blockTime,
				Status:         StatusError,
				Quota:          QuotaState{Exceeded: true, NextRecoverAt: blockTime},
			},
		}},
	}

	// Pick should return cooldown error
	_, err := selector.Pick(context.Background(), "gemini", "model1", cliproxyexecutor.Options{}, auths)
	if err == nil {
		t.Fatal("Pick() should return error when all auths are blocked")
	}

	cooldownErr, ok := err.(*modelCooldownError)
	if !ok {
		t.Fatalf("Error should be *modelCooldownError, got %T: %v", err, err)
	}

	if cooldownErr.model != "model1" {
		t.Errorf("cooldownErr.model = %s, want model1", cooldownErr.model)
	}

	// Verify the error has reasonable reset time
	if cooldownErr.resetIn < 29*time.Minute {
		t.Errorf("cooldownErr.resetIn = %v, should be at least 29min", cooldownErr.resetIn)
	}
}

func TestFillFirstSelector_BlockedAuthSkipped(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	now := time.Now()

	// Create auths with first one blocked
	auths := []*Auth{
		{ID: "auth1", Status: StatusActive, ModelStates: map[string]*ModelState{
			"model1": {
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Status:         StatusError,
			},
		}},
		{ID: "auth2", Status: StatusActive},
		{ID: "auth3", Status: StatusActive},
	}

	// FillFirst should skip blocked auth1 and return auth2
	got, err := selector.Pick(context.Background(), "gemini", "model1", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "auth2" {
		t.Fatalf("Pick() got %s, want auth2 (auth1 should be blocked)", got.ID)
	}
}

func TestRoundRobinSelector_BlockRecoveryAfterTimeout(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}

	// auth1 was blocked but block time has passed
	pastTime := time.Now().Add(-1 * time.Minute) // 1 minute in the past

	auths := []*Auth{
		{ID: "auth1", Status: StatusActive, ModelStates: map[string]*ModelState{
			"model1": {
				Unavailable:    true,
				NextRetryAfter: pastTime, // Already expired
				Status:         StatusError,
			},
		}},
		{ID: "auth2", Status: StatusActive},
	}

	// auth1 should be available again since NextRetryAfter is in the past
	got, err := selector.Pick(context.Background(), "gemini", "model1", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	// Should include auth1 in rotation since block expired
	if got.ID != "auth1" {
		t.Fatalf("Pick() got %s, want auth1 (block should have expired)", got.ID)
	}
}

func TestRoundRobinSelector_ConcurrentBlockHandling(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	now := time.Now()

	auths := []*Auth{
		{ID: "auth1", Status: StatusActive},
		{ID: "auth2", Status: StatusActive},
		{ID: "auth3", Status: StatusActive, ModelStates: map[string]*ModelState{
			"model1": {
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Status:         StatusError,
			},
		}},
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 100)
	results := make(chan string, 100)

	// Concurrent picks - should never return auth3
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := selector.Pick(context.Background(), "gemini", "model1", cliproxyexecutor.Options{}, auths)
			if err != nil {
				errCh <- err
				return
			}
			results <- got.ID
		}()
	}

	wg.Wait()
	close(errCh)
	close(results)

	for err := range errCh {
		t.Fatalf("Concurrent Pick() error = %v", err)
	}

	for id := range results {
		if id == "auth3" {
			t.Fatal("Pick() returned blocked auth3")
		}
	}
}

func TestMarkResult_30MinBlockForServerErrors(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})

	auth1 := &Auth{ID: "auth1", Provider: "gemini", Status: StatusActive}
	ctx := context.Background()
	manager.Register(ctx, auth1)

	now := time.Now()

	testCases := []struct {
		name        string
		statusCode  int
		expectBlock bool
	}{
		{"408 Request Timeout", 408, true},
		{"500 Internal Server Error", 500, true},
		{"502 Bad Gateway", 502, true},
		{"503 Service Unavailable", 503, true},
		{"504 Gateway Timeout", 504, true},
		{"Generic Error (999)", 999, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset auth state
			manager.mu.Lock()
			if a, ok := manager.auths["auth1"]; ok {
				a.ModelStates = nil
				a.Unavailable = false
			}
			manager.mu.Unlock()

			result := Result{
				AuthID:   "auth1",
				Provider: "gemini",
				Model:    "model1",
				Success:  false,
				Error:    &Error{Code: "test", Message: "test error", HTTPStatus: tc.statusCode},
			}
			manager.MarkResult(ctx, result)

			// Check state
			manager.mu.RLock()
			auth, _ := manager.auths["auth1"]
			manager.mu.RUnlock()

			if auth.ModelStates == nil {
				t.Fatal("ModelStates should not be nil after failure")
			}

			state := auth.ModelStates["model1"]
			if state == nil {
				t.Fatal("model1 state should not be nil")
			}

			if tc.expectBlock {
				if !state.Unavailable {
					t.Error("auth should be unavailable")
				}
				blockDuration := state.NextRetryAfter.Sub(now)
				if blockDuration < 29*time.Minute {
					t.Errorf("Block duration %v < 29min", blockDuration)
				}
				if blockDuration > 31*time.Minute {
					t.Errorf("Block duration %v > 31min", blockDuration)
				}
			}
		})
	}
}

func TestMarkResult_SuccessResetsBlock(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})

	now := time.Now()
	auth1 := &Auth{
		ID:       "auth1",
		Provider: "gemini",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"model1": {
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Status:         StatusError,
			},
		},
	}

	ctx := context.Background()
	manager.Register(ctx, auth1)

	// Mark as success
	result := Result{
		AuthID:   "auth1",
		Provider: "gemini",
		Model:    "model1",
		Success:  true,
	}
	manager.MarkResult(ctx, result)

	// Check state is reset
	manager.mu.RLock()
	auth, _ := manager.auths["auth1"]
	manager.mu.RUnlock()

	state := auth.ModelStates["model1"]
	if state.Unavailable {
		t.Error("auth should be available after success")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Error("NextRetryAfter should be zero after success")
	}
	if state.Status != StatusActive {
		t.Errorf("Status should be StatusActive, got %v", state.Status)
	}
}

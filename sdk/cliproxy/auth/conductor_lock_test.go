package auth

import (
	"context"
	"testing"
	"time"
)

type lockProbeStore struct {
	manager *Manager
	started chan struct{}
	saves   chan *Auth
}

func (s *lockProbeStore) List(context.Context) ([]*Auth, error) {
	return nil, nil
}

func (s *lockProbeStore) Save(_ context.Context, auth *Auth) (string, error) {
	if s.saves != nil {
		s.saves <- auth
	}
	close(s.started)
	if _, ok := s.manager.Executor("codex"); !ok {
		return "", nil
	}
	return "", nil
}

func (s *lockProbeStore) Delete(context.Context, string) error {
	return nil
}

func TestManagerMarkResultPersistsOutsideWriteLock(t *testing.T) {
	// Given
	store := &lockProbeStore{started: make(chan struct{})}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	store.manager = manager
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "token"},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	// When
	done := make(chan struct{})
	go func() {
		manager.MarkResult(context.Background(), Result{AuthID: "auth-1", Provider: "codex", Model: "gpt-5", Success: true})
		close(done)
	}()

	// Then
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("store Save() was not called")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("MarkResult() held manager write lock while persisting")
	}
}

func TestManagerMarkResultSkipsPersistForUnknownAuth(t *testing.T) {
	// Given
	store := &lockProbeStore{started: make(chan struct{}), saves: make(chan *Auth, 1)}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	store.manager = manager

	// When
	manager.MarkResult(context.Background(), Result{AuthID: "missing-auth", Provider: "codex", Model: "gpt-5", Success: true})

	// Then
	select {
	case <-store.saves:
		t.Fatal("MarkResult() persisted a nil or missing auth snapshot")
	default:
	}
}

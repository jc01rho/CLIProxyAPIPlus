package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingStore struct {
	saveCount atomic.Int32
}

func (s *countingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *countingStore) Save(context.Context, *Auth) (string, error) {
	s.saveCount.Add(1)
	return "", nil
}

func (s *countingStore) Delete(context.Context, string) error { return nil }

type blockingSaveStore struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingSaveStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *blockingSaveStore) Save(context.Context, *Auth) (string, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return "", nil
}

func (s *blockingSaveStore) Delete(context.Context, string) error { return nil }

func TestMarkResultDoesNotHoldManagerLockWhilePersisting(t *testing.T) {
	store := &blockingSaveStore{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	mgr := NewManager(store, nil, nil)
	mgr.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	auth := &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "codex"},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		mgr.MarkResult(context.Background(), Result{AuthID: "auth-1", Provider: "codex", Success: true})
		close(done)
	}()

	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("MarkResult did not enter persistence")
	}

	executorDone := make(chan bool, 1)
	go func() {
		_, ok := mgr.Executor("codex")
		executorDone <- ok
	}()

	select {
	case ok := <-executorDone:
		if !ok {
			t.Fatal("Executor(codex) returned false")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Executor blocked while MarkResult was persisting")
	}

	close(store.release)
	<-done
}

func TestWithSkipPersist_DisablesUpdatePersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls, got %d", got)
	}

	if _, err := mgr.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected 1 Save call, got %d", got)
	}

	ctxSkip := WithSkipPersist(context.Background())
	if _, err := mgr.Update(ctxSkip, auth); err != nil {
		t.Fatalf("Update(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected Save call count to remain 1, got %d", got)
	}
}

func TestWithSkipPersist_DisablesRegisterPersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls, got %d", got)
	}
}

func TestPersist_SkipsConfigAPIKeyAuth(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "codex:apikey:abc",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "secret",
			"source":  "config:codex[abc]",
		},
		Metadata: map[string]any{"disable_cooling": true},
	}
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls for config api key, got %d", got)
	}
	mgr.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Success: true})
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected MarkResult to skip persist for config api key, got %d Save calls", got)
	}
}

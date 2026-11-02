package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// armedSaveStore blocks the first Save after arm() until release is closed.
type armedSaveStore struct {
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func newArmedSaveStore() *armedSaveStore {
	return &armedSaveStore{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *armedSaveStore) arm() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

func (s *armedSaveStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *armedSaveStore) Delete(context.Context, string) error { return nil }

func (s *armedSaveStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	block := s.armed
	s.armed = false
	s.mu.Unlock()
	if block {
		close(s.entered)
		<-s.release
	}
	return auth.ID, nil
}

// MarkResult commits a cooldown into m.auths and then releases m.mu around
// Store.Save; a concurrent pick in that window must not select the cooling auth.
func TestPickNextSkipsCoolingAuthWhileResultIsPersisted(t *testing.T) {
	ctx := context.Background()
	const model = "persist-window-model"
	store := newArmedSaveStore()
	m := NewManager(store, &RoundRobinSelector{}, nil)
	m.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})
	registerSchedulerModels(t, "gemini", model, "cool-a")
	if _, errRegister := m.Register(ctx, &Auth{ID: "cool-a", Provider: "gemini", Metadata: map[string]any{"email": "a@example.com"}}); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	m.RefreshSchedulerEntry("cool-a")
	if got, _, errPick := m.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil); errPick != nil || got == nil {
		t.Fatalf("baseline pick = %v, %v", got, errPick)
	}

	store.arm()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.MarkResult(ctx, Result{AuthID: "cool-a", Provider: "gemini", Model: model, Success: false,
			Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}})
	}()
	select {
	case <-store.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("MarkResult never reached Store.Save")
	}
	defer func() {
		close(store.release)
		<-done
	}()

	live, ok := m.GetByID("cool-a")
	if !ok {
		t.Fatal("auth missing")
	}
	blocked, reason, next := isAuthBlockedForModel(live, model, time.Now())
	if !blocked {
		t.Fatalf("precondition: manager state is not cooling yet (reason=%v)", reason)
	}
	got, _, errPick := m.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick == nil && got != nil && got.ID == "cool-a" {
		t.Fatalf("fast path selected %s while m.auths marks it blocked (reason=%v until %s) during Store.Save", got.ID, reason, next.Format(time.RFC3339))
	}
}

package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type affinityBlockExecutor struct {
	mu      sync.Mutex
	blocked map[string]bool
	calls   []string
}

func (e *affinityBlockExecutor) Identifier() string { return "affinity-block" }

func (e *affinityBlockExecutor) record(auth *Auth) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID)
	if e.blocked[auth.ID] {
		return &Error{HTTPStatus: http.StatusForbidden, Message: cloudflareEdgeBlockPage}
	}
	return nil
}

func (e *affinityBlockExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.record(auth); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *affinityBlockExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if err := e.record(auth); err != nil {
		return nil, err
	}
	return nil, nil
}

func (*affinityBlockExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *affinityBlockExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(context.Background(), auth, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
}

func (*affinityBlockExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *affinityBlockExecutor) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

func TestSessionAffinitySkipsCloudflareBlockedCredential(t *testing.T) {
	ctx := context.Background()
	const model = "affinity-block-model"
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: SessionAffinityRebindOnNever,
	})
	t.Cleanup(selector.Stop)
	manager := NewManager(nil, selector, nil)
	manager.SetRetryConfig(3, 0, 0)
	executor := &affinityBlockExecutor{blocked: map[string]bool{}}
	manager.RegisterExecutor(executor)

	for _, candidate := range []*Auth{
		{ID: "affinity-a", Provider: "affinity-block", Status: StatusActive, Attributes: map[string]string{"priority": "10"}, Metadata: map[string]any{"disable_cooling": true}},
		{ID: "affinity-z", Provider: "affinity-block", Status: StatusActive, Attributes: map[string]string{"priority": "5"}, Metadata: map[string]any{"disable_cooling": true}},
	} {
		registry.GetGlobalRegistry().RegisterClient(candidate.ID, "affinity-block", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
		if _, err := manager.Register(ctx, candidate); err != nil {
			t.Fatal(err)
		}
	}

	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"affinity-block-session"}}}
	req := cliproxyexecutor.Request{Model: model}
	first, err := manager.Execute(ctx, []string{"affinity-block"}, req, opts)
	if err != nil {
		t.Fatalf("first execution error = %v", err)
	}
	t.Logf("first request served by %s", first.Payload)

	executor.mu.Lock()
	executor.blocked["affinity-a"] = true
	executor.calls = nil
	executor.mu.Unlock()

	second, err := manager.Execute(ctx, []string{"affinity-block"}, req, opts)
	if err != nil {
		t.Fatalf("second execution error = %v", err)
	}
	third, err := manager.Execute(ctx, []string{"affinity-block"}, req, opts)
	if err != nil {
		t.Fatalf("third execution error = %v", err)
	}
	calls := executor.snapshot()
	t.Logf("second=%s third=%s calls=%v", second.Payload, third.Payload, calls)
	blockedCalls := 0
	for _, id := range calls {
		if id == "affinity-a" {
			blockedCalls++
		}
	}
	if blockedCalls != 1 {
		t.Fatalf("blocked credential selected %d times after the block started, want only the discovering call; calls=%v", blockedCalls, calls)
	}
	if string(third.Payload) == "affinity-a" {
		t.Fatalf("third request re-selected the blocked credential; calls=%v", calls)
	}
}

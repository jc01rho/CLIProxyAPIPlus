package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// contextCeilingTestExecutor counts upstream dispatches so a test can prove a
// refused request never left the proxy.
type contextCeilingTestExecutor struct {
	calls int
}

func (*contextCeilingTestExecutor) Identifier() string { return "claude" }

func (e *contextCeilingTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls++
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *contextCeilingTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls++
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *contextCeilingTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("{}")}, nil
}

func (e *contextCeilingTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *contextCeilingTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// contextCeilingTestManager registers one claude credential and advertises the
// model with contextLength, so the test owns the catalog value the gate reads.
func contextCeilingTestManager(t *testing.T, exec *contextCeilingTestExecutor, model string, contextLength int) *Manager {
	t.Helper()
	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(exec)
	auth := &Auth{
		ID:         "context-ceiling-auth-" + model,
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata:   map[string]any{"access_token": "token"},
	}
	info := &registry.ModelInfo{ID: model, Type: "claude"}
	if contextLength > 0 {
		info.ContextLength = contextLength
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{info})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	return manager
}

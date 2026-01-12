package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestManager_FallbackToNextModel(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	manager.SetFallbackConfig(
		map[string]string{"model-a": "model-b"},
		nil,
	)

	availableAuth := &Auth{
		ID:       "fallback-test-auth1",
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "test@example.com"},
	}

	if _, err := manager.Register(context.Background(), availableAuth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(availableAuth.ID, availableAuth.Provider, []*registry.ModelInfo{
		{ID: "model-a"},
		{ID: "model-b"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(availableAuth.ID)
	})

	mockExecutor := &mockFallbackExecutor{
		identifier: "test-provider",
		modelResults: map[string]modelResult{
			"model-a": {err: newModelCooldownError("model-a", "test-provider", time.Minute)},
			"model-b": {resp: cliproxyexecutor.Response{Payload: []byte(`{"model":"model-b"}`)}},
		},
	}
	manager.RegisterExecutor(mockExecutor)

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if string(resp.Payload) != `{"model":"model-b"}` {
		t.Errorf("Execute() response = %s, want model-b response", string(resp.Payload))
	}
}

func TestManager_FallbackChainTraversal(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	manager.SetFallbackConfig(
		nil,
		[]string{"model-a", "model-b", "model-c"},
	)

	availableAuth := &Auth{
		ID:       "fallback-chain-auth1",
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "test@example.com"},
	}

	if _, err := manager.Register(context.Background(), availableAuth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(availableAuth.ID, availableAuth.Provider, []*registry.ModelInfo{
		{ID: "model-a"},
		{ID: "model-b"},
		{ID: "model-c"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(availableAuth.ID)
	})

	mockExecutor := &mockFallbackExecutor{
		identifier: "test-provider",
		modelResults: map[string]modelResult{
			"model-a": {err: newModelCooldownError("model-a", "test-provider", time.Minute)},
			"model-b": {err: newModelCooldownError("model-b", "test-provider", time.Minute)},
			"model-c": {resp: cliproxyexecutor.Response{Payload: []byte(`{"model":"model-c"}`)}},
		},
	}
	manager.RegisterExecutor(mockExecutor)

	req := cliproxyexecutor.Request{Model: "model-a"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if string(resp.Payload) != `{"model":"model-c"}` {
		t.Errorf("Execute() response = %s, want model-c response", string(resp.Payload))
	}
}

func TestManager_FallbackMaxDepth(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	fallbackModels := make(map[string]string)
	models := make([]*registry.ModelInfo, 25)
	for i := 0; i < 24; i++ {
		from := "model-" + string(rune('a'+i))
		to := "model-" + string(rune('a'+i+1))
		fallbackModels[from] = to
		models[i] = &registry.ModelInfo{ID: from}
	}
	models[24] = &registry.ModelInfo{ID: "model-" + string(rune('a'+24))}

	manager.SetFallbackConfig(fallbackModels, nil)

	availableAuth := &Auth{
		ID:       "fallback-depth-auth1",
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "test@example.com"},
	}

	if _, err := manager.Register(context.Background(), availableAuth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(availableAuth.ID, availableAuth.Provider, models)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(availableAuth.ID)
	})

	mockExecutor := &mockFallbackExecutor{
		identifier:  "test-provider",
		alwaysFail:  true,
		failWithErr: newModelCooldownError("test", "test-provider", time.Minute),
	}
	manager.RegisterExecutor(mockExecutor)

	req := cliproxyexecutor.Request{Model: "model-a"}
	_, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err == nil {
		t.Fatal("Execute() error = nil, want cooldown error")
	}

	_, isCooldown := err.(*modelCooldownError)
	if !isCooldown {
		t.Errorf("Execute() error type = %T, want *modelCooldownError", err)
	}
}

func TestManager_NoFallbackConfigured(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	manager.SetFallbackConfig(nil, nil)

	availableAuth := &Auth{
		ID:       "no-fallback-auth1",
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "test@example.com"},
	}

	if _, err := manager.Register(context.Background(), availableAuth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(availableAuth.ID, availableAuth.Provider, []*registry.ModelInfo{
		{ID: "model-a"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(availableAuth.ID)
	})

	mockExecutor := &mockFallbackExecutor{
		identifier:  "test-provider",
		alwaysFail:  true,
		failWithErr: newModelCooldownError("model-a", "test-provider", time.Minute),
	}
	manager.RegisterExecutor(mockExecutor)

	req := cliproxyexecutor.Request{Model: "model-a"}
	_, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}

	_, isCooldown := err.(*modelCooldownError)
	if !isCooldown {
		t.Errorf("Execute() error type = %T, want *modelCooldownError", err)
	}
}

func TestManager_GetFallbackConfig(t *testing.T) {
	manager := NewManager(nil, nil, nil)

	models, chain := manager.GetFallbackConfig()
	if models != nil {
		t.Errorf("GetFallbackConfig() models = %v, want nil", models)
	}
	if chain != nil {
		t.Errorf("GetFallbackConfig() chain = %v, want nil", chain)
	}

	manager.SetFallbackConfig(
		map[string]string{"a": "b"},
		[]string{"x", "y", "z"},
	)

	models, chain = manager.GetFallbackConfig()
	if models == nil || models["a"] != "b" {
		t.Errorf("GetFallbackConfig() models = %v, want {a:b}", models)
	}
	if len(chain) != 3 || chain[0] != "x" {
		t.Errorf("GetFallbackConfig() chain = %v, want [x y z]", chain)
	}
}

type modelResult struct {
	resp cliproxyexecutor.Response
	err  error
}

type mockFallbackExecutor struct {
	identifier   string
	modelResults map[string]modelResult
	alwaysFail   bool
	failWithErr  error
}

func (e *mockFallbackExecutor) Identifier() string {
	return e.identifier
}

func (e *mockFallbackExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.alwaysFail {
		return cliproxyexecutor.Response{}, e.failWithErr
	}
	if result, ok := e.modelResults[req.Model]; ok {
		return result.resp, result.err
	}
	return cliproxyexecutor.Response{}, newModelCooldownError(req.Model, e.identifier, time.Minute)
}

func (e *mockFallbackExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	if e.alwaysFail {
		return nil, e.failWithErr
	}
	if result, ok := e.modelResults[req.Model]; ok {
		if result.err != nil {
			return nil, result.err
		}
		ch := make(chan cliproxyexecutor.StreamChunk)
		close(ch)
		return ch, nil
	}
	return nil, newModelCooldownError(req.Model, e.identifier, time.Minute)
}

func (e *mockFallbackExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *mockFallbackExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *mockFallbackExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

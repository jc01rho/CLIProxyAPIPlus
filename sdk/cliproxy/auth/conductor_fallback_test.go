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

func TestGetFallbackModel_Chain(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	fallbackModels := map[string]string{
		"opus":   "sonnet",
		"sonnet": "glm-4.7",
	}
	manager.SetFallbackConfig(fallbackModels, nil)

	tests := []struct {
		name     string
		model    string
		expected string
	}{
		{"opus falls back to sonnet", "opus", "sonnet"},
		{"sonnet falls back to glm-4.7", "sonnet", "glm-4.7"},
		{"glm-4.7 has no fallback", "glm-4.7", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := manager.getFallbackModel(tt.model, make(map[string]bool))
			if result != tt.expected {
				t.Errorf("getFallbackModel(%q) = %q, want %q", tt.model, result, tt.expected)
			}
		})
	}
}

// TestGetFallbackModel_NoFallback verifies that models without fallback configuration return empty string
func TestGetFallbackModel_NoFallback(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	fallbackModels := map[string]string{
		"opus":   "sonnet",
		"sonnet": "glm-4.7",
	}
	manager.SetFallbackConfig(fallbackModels, nil)

	tests := []struct {
		name     string
		model    string
		expected string
	}{
		{"unknown-model has no fallback", "unknown-model", ""},
		{"gpt-4o has no fallback", "gpt-4o", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := manager.getFallbackModel(tt.model, make(map[string]bool))
			if result != tt.expected {
				t.Errorf("getFallbackModel(%q) = %q, want %q", tt.model, result, tt.expected)
			}
		})
	}
}

// TestExecuteWithFallback_RateLimitCascade verifies the fallback chain opus → sonnet → glm-4.7
// when each model returns modelCooldownError (rate-limited)
func TestExecuteWithFallback_RateLimitCascade(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	// Configure fallback chain: opus → sonnet → glm-4.7
	fallbackModels := map[string]string{
		"opus":   "sonnet",
		"sonnet": "glm-4.7",
	}
	manager.SetFallbackConfig(fallbackModels, nil)

	// Register auth
	availableAuth := &Auth{
		ID:       "fallback-cascade-auth",
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "test@example.com"},
	}

	if _, err := manager.Register(context.Background(), availableAuth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}

	// Register models in registry
	registry.GetGlobalRegistry().RegisterClient(availableAuth.ID, availableAuth.Provider, []*registry.ModelInfo{
		{ID: "opus"},
		{ID: "sonnet"},
		{ID: "glm-4.7"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(availableAuth.ID)
	})

	// Track which models were attempted via executor
	var modelsAttempted []string

	// Mock executor that tracks models and returns cooldown for opus/sonnet, success for glm-4.7
	mockExecutor := &trackingFallbackExecutor{
		identifier:      "test-provider",
		modelsAttempted: &modelsAttempted,
		modelResults: map[string]modelResult{
			"opus":    {err: newModelCooldownError("opus", "test-provider", time.Minute)},
			"sonnet":  {err: newModelCooldownError("sonnet", "test-provider", time.Minute)},
			"glm-4.7": {resp: cliproxyexecutor.Response{Payload: []byte(`{"model":"glm-4.7"}`)}},
		},
	}
	manager.RegisterExecutor(mockExecutor)

	// Execute with model "opus"
	req := cliproxyexecutor.Request{Model: "opus"}
	resp, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	// Should succeed with glm-4.7 response
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if string(resp.Payload) != `{"model":"glm-4.7"}` {
		t.Errorf("Execute() response = %s, want glm-4.7 response", string(resp.Payload))
	}

	// Verify fallback chain was traversed: opus → sonnet → glm-4.7
	expectedModels := []string{"opus", "sonnet", "glm-4.7"}
	if len(modelsAttempted) != len(expectedModels) {
		t.Errorf("Models attempted = %v, want %v", modelsAttempted, expectedModels)
	} else {
		for i, model := range expectedModels {
			if modelsAttempted[i] != model {
				t.Errorf("Model at position %d = %q, want %q", i, modelsAttempted[i], model)
			}
		}
	}
}

// TestExecuteWithFallback_AuthUnavailableNoFallback verifies that auth_unavailable error
// does NOT trigger fallback (only modelCooldownError triggers fallback)
func TestExecuteWithFallback_AuthUnavailableNoFallback(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	// Configure fallback chain: opus → sonnet → glm-4.7
	fallbackModels := map[string]string{
		"opus":   "sonnet",
		"sonnet": "glm-4.7",
	}
	manager.SetFallbackConfig(fallbackModels, nil)

	// Register auth
	availableAuth := &Auth{
		ID:       "no-fallback-auth-unavailable",
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "test@example.com"},
	}

	if _, err := manager.Register(context.Background(), availableAuth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}

	// Register models in registry
	registry.GetGlobalRegistry().RegisterClient(availableAuth.ID, availableAuth.Provider, []*registry.ModelInfo{
		{ID: "opus"},
		{ID: "sonnet"},
		{ID: "glm-4.7"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(availableAuth.ID)
	})

	// Track which models were attempted
	var modelsAttempted []string

	// Mock executor that returns auth_unavailable error for opus (NOT modelCooldownError)
	mockExecutor := &trackingFallbackExecutor{
		identifier:      "test-provider",
		modelsAttempted: &modelsAttempted,
		modelResults: map[string]modelResult{
			"opus": {err: &Error{Code: "auth_unavailable", Message: "no auth available"}},
		},
	}
	manager.RegisterExecutor(mockExecutor)

	// Execute with model "opus"
	req := cliproxyexecutor.Request{Model: "opus"}
	_, err := manager.Execute(context.Background(), []string{"test-provider"}, req, cliproxyexecutor.Options{})

	// Should fail with auth_unavailable error (no fallback)
	if err == nil {
		t.Fatal("Execute() error = nil, want auth_unavailable error")
	}

	// Verify error is auth_unavailable
	authErr, ok := err.(*Error)
	if !ok {
		t.Errorf("Execute() error type = %T, want *Error", err)
	} else if authErr.Code != "auth_unavailable" {
		t.Errorf("Execute() error code = %q, want %q", authErr.Code, "auth_unavailable")
	}

	// Verify ONLY opus was attempted (no fallback to sonnet or glm-4.7)
	if len(modelsAttempted) != 1 {
		t.Errorf("Models attempted = %v, want only [opus]", modelsAttempted)
	} else if modelsAttempted[0] != "opus" {
		t.Errorf("Model attempted = %q, want %q", modelsAttempted[0], "opus")
	}
}

// trackingFallbackExecutor is a mock executor that tracks which models were attempted
type trackingFallbackExecutor struct {
	identifier      string
	modelsAttempted *[]string
	modelResults    map[string]modelResult
}

func (e *trackingFallbackExecutor) Identifier() string {
	return e.identifier
}

func (e *trackingFallbackExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Track the model that was attempted
	*e.modelsAttempted = append(*e.modelsAttempted, req.Model)

	if result, ok := e.modelResults[req.Model]; ok {
		return result.resp, result.err
	}
	return cliproxyexecutor.Response{}, &Error{Code: "model_not_found", Message: "model not configured in mock"}
}

func (e *trackingFallbackExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	*e.modelsAttempted = append(*e.modelsAttempted, req.Model)

	if result, ok := e.modelResults[req.Model]; ok {
		if result.err != nil {
			return nil, result.err
		}
		ch := make(chan cliproxyexecutor.StreamChunk)
		close(ch)
		return ch, nil
	}
	return nil, &Error{Code: "model_not_found", Message: "model not configured in mock"}
}

func (e *trackingFallbackExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *trackingFallbackExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *trackingFallbackExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

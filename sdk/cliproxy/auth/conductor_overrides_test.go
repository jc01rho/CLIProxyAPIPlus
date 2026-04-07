package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

const requestScopedNotFoundMessage = "Item with id 'rs_0b5f3eb6f51f175c0169ca74e4a85881998539920821603a74' not found. Items are not persisted when `store` is set to false. Try again with `store` set to true, or remove this item from your input."

func TestManager_ShouldRetryAfterError_RespectsAuthRequestRetryOverride(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	model := "test-model"
	next := time.Now().Add(5 * time.Second)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{
			"request_retry": float64(0),
		},
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 0, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("expected shouldRetry=false for request_retry=0, got true (wait=%v)", wait)
	}

	auth.Metadata["request_retry"] = float64(1)
	if _, errUpdate := m.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	wait, shouldRetry = m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 0, []string{"claude"}, model, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true for request_retry=1, got false")
	}
	if wait <= 0 {
		t.Fatalf("expected wait > 0, got %v", wait)
	}

	_, shouldRetry = m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 1, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("expected shouldRetry=false on attempt=1 for request_retry=1, got true")
	}
}

type credentialRetryLimitExecutor struct {
	id string

	mu    sync.Mutex
	calls int
}

type priorityFallbackExecutor struct {
	id string

	mu         sync.Mutex
	callOrder  []string
	failAuthID map[string]struct{}
}

func (e *credentialRetryLimitExecutor) Identifier() string {
	return e.id
}

func (e *credentialRetryLimitExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall()
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.recordCall()
	return nil, &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *credentialRetryLimitExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall()
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *credentialRetryLimitExecutor) recordCall() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
}

func (e *credentialRetryLimitExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *priorityFallbackExecutor) Identifier() string {
	return e.id
}

func (e *priorityFallbackExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.callOrder = append(e.callOrder, authID)
	_, shouldFail := e.failAuthID[authID]
	e.mu.Unlock()
	if shouldFail {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: 500, Message: "boom"}
	}
	return cliproxyexecutor.Response{}, nil
}

func (e *priorityFallbackExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_, err := e.Execute(context.Background(), auth, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
	if err != nil {
		return nil, err
	}
	return &cliproxyexecutor.StreamResult{}, nil
}

func (e *priorityFallbackExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *priorityFallbackExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(context.Background(), auth, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
}

func (e *priorityFallbackExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *priorityFallbackExecutor) CallOrder() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.callOrder))
	copy(out, e.callOrder)
	return out
}

type authFallbackExecutor struct {
	id string

	mu                sync.Mutex
	executeCalls      []string
	streamCalls       []string
	executeErrors     map[string]error
	streamFirstErrors map[string]error
}

func (e *authFallbackExecutor) Identifier() string {
	return e.id
}

func (e *authFallbackExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	err := e.executeErrors[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *authFallbackExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	err := e.streamFirstErrors[auth.ID]
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if err != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: err}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *authFallbackExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *authFallbackExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 500, Message: "not implemented"}
}

func (e *authFallbackExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *authFallbackExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func (e *authFallbackExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func newCredentialRetryLimitTestManager(t *testing.T, maxRetryCredentials int) (*Manager, *credentialRetryLimitExecutor) {
	t.Helper()

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, maxRetryCredentials)

	executor := &credentialRetryLimitExecutor{id: "claude"}
	m.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth1 := &Auth{ID: baseID + "-auth-1", Provider: "claude"}
	auth2 := &Auth{ID: baseID + "-auth-2", Provider: "claude"}

	// Auth selection requires that the global model registry knows each credential supports the model.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, errRegister := m.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	return m, executor
}

func TestManager_MaxRetryCredentials_LimitsCrossCredentialRetries(t *testing.T) {
	request := cliproxyexecutor.Request{Model: "test-model"}
	testCases := []struct {
		name   string
		invoke func(*Manager) error
	}{
		{
			name: "execute",
			invoke: func(m *Manager) error {
				_, errExecute := m.Execute(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "execute_count",
			invoke: func(m *Manager) error {
				_, errExecute := m.ExecuteCount(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "execute_stream",
			invoke: func(m *Manager) error {
				_, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			limitedManager, limitedExecutor := newCredentialRetryLimitTestManager(t, 1)
			if errInvoke := tc.invoke(limitedManager); errInvoke == nil {
				t.Fatalf("expected error for limited retry execution")
			}
			if calls := limitedExecutor.Calls(); calls != 1 {
				t.Fatalf("expected 1 call with max-retry-credentials=1, got %d", calls)
			}

			unlimitedManager, unlimitedExecutor := newCredentialRetryLimitTestManager(t, 0)
			if errInvoke := tc.invoke(unlimitedManager); errInvoke == nil {
				t.Fatalf("expected error for unlimited retry execution")
			}
			if calls := unlimitedExecutor.Calls(); calls != 2 {
				t.Fatalf("expected 2 calls with max-retry-credentials=0, got %d", calls)
			}
		})
	}
}

func TestManagerExecute_FallsBackToLowerPriorityBucketAfterHigherPriorityExhausted(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)

	executor := &priorityFallbackExecutor{
		id: "claude",
		failAuthID: map[string]struct{}{
			"high-a": {},
			"high-b": {},
		},
	}
	manager.RegisterExecutor(executor)

	model := "test-model"
	reg := registry.GetGlobalRegistry()
	for _, authID := range []string{"high-a", "high-b", "low-a", "low-b"} {
		reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range []string{"high-a", "high-b", "low-a", "low-b"} {
			reg.UnregisterClient(authID)
		}
	})

	for _, auth := range []*Auth{
		{ID: "high-a", Provider: "claude", Attributes: map[string]string{"priority": "10"}},
		{ID: "high-b", Provider: "claude", Attributes: map[string]string{"priority": "10"}},
		{ID: "low-a", Provider: "claude", Attributes: map[string]string{"priority": "5"}},
		{ID: "low-b", Provider: "claude", Attributes: map[string]string{"priority": "5"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	_, errExecute := manager.Execute(
		context.Background(),
		[]string{"claude"},
		cliproxyexecutor.Request{Model: model},
		cliproxyexecutor.Options{},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if got, want := executor.CallOrder(), []string{"high-a", "high-b", "low-a"}; len(got) != len(want) {
		t.Fatalf("Execute() call order length = %d, want %d (got %v)", len(got), len(want), got)
	} else {
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("Execute() call order[%d] = %q, want %q (full=%v)", index, got[index], want[index], got)
			}
		}
	}
}

func TestManagerExecute_ThresholdRoutingFiltersByBillingClass(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			TokenThresholdRules: []internalconfig.TokenThresholdRule{{
				ModelPattern: "test-*",
				MaxTokens:    100,
				BillingClass: internalconfig.BillingClassMetered,
				Enabled:      true,
			}},
		},
	})

	executor := &priorityFallbackExecutor{id: "claude", failAuthID: map[string]struct{}{}}
	manager.RegisterExecutor(executor)

	model := "test-model"
	baseID := uuid.NewString()
	meteredID := baseID + "-metered-auth"
	perRequestID := baseID + "-per-request-auth"
	reg := registry.GetGlobalRegistry()
	for _, authID := range []string{meteredID, perRequestID} {
		reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range []string{meteredID, perRequestID} {
			reg.UnregisterClient(authID)
		}
	})

	for _, auth := range []*Auth{
		{ID: meteredID, Provider: "claude", Attributes: map[string]string{"billing_class": "metered", "priority": "1"}},
		{ID: perRequestID, Provider: "claude", Attributes: map[string]string{"billing_class": "per-request", "priority": "10"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	_, errExecute := manager.Execute(
		context.Background(),
		[]string{"claude"},
		cliproxyexecutor.Request{Model: model},
		cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: 50}},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	order := executor.CallOrder()
	if len(order) != 1 || order[0] != meteredID {
		t.Fatalf("expected only metered auth to be used, got %v", order)
	}
}

func TestManagerExecute_ThresholdRoutingFiltersAcrossConfigAndFileBackedAuth(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			TokenThresholdRules: []internalconfig.TokenThresholdRule{{
				ModelPattern: "test-*",
				MaxTokens:    100,
				BillingClass: internalconfig.BillingClassPerRequest,
				Enabled:      true,
			}},
		},
	})

	executor := &priorityFallbackExecutor{id: "claude", failAuthID: map[string]struct{}{}}
	manager.RegisterExecutor(executor)

	model := "test-model"
	baseID := uuid.NewString()
	configID := baseID + "-config-auth"
	oauthID := baseID + "-oauth-auth"
	reg := registry.GetGlobalRegistry()
	for _, authID := range []string{configID, oauthID} {
		reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range []string{configID, oauthID} {
			reg.UnregisterClient(authID)
		}
	})

	for _, auth := range []*Auth{
		{ID: configID, Provider: "claude", Attributes: map[string]string{"billing_class": "metered", "priority": "100", "auth_kind": "apikey"}},
		{ID: oauthID, Provider: "claude", Attributes: map[string]string{"billing_class": "per-request", "priority": "1", "auth_kind": "oauth"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	_, errExecute := manager.Execute(
		context.Background(),
		[]string{"claude"},
		cliproxyexecutor.Request{Model: model},
		cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: 50}},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	order := executor.CallOrder()
	if len(order) != 1 || order[0] != oauthID {
		t.Fatalf("expected only oauth per-request auth to be used, got %v", order)
	}
}

func TestManagerExecute_ThresholdRoutingRecordsBillingDecisionInGinContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			TokenThresholdRules: []internalconfig.TokenThresholdRule{{
				ModelPattern: "test-*",
				MaxTokens:    100,
				BillingClass: internalconfig.BillingClassMetered,
				Enabled:      true,
			}},
		},
	})

	executor := &priorityFallbackExecutor{id: "claude", failAuthID: map[string]struct{}{}}
	manager.RegisterExecutor(executor)

	model := "test-model"
	baseID := uuid.NewString()
	meteredID := baseID + "-metered-auth"
	perRequestID := baseID + "-per-request-auth"
	reg := registry.GetGlobalRegistry()
	for _, authID := range []string{meteredID, perRequestID} {
		reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range []string{meteredID, perRequestID} {
			reg.UnregisterClient(authID)
		}
	})

	for _, auth := range []*Auth{
		{ID: meteredID, Provider: "claude", Attributes: map[string]string{"billing_class": "metered", "priority": "1"}},
		{ID: perRequestID, Provider: "claude", Attributes: map[string]string{"billing_class": "per-request", "priority": "10"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	_, errExecute := manager.Execute(
		ctx,
		[]string{"claude"},
		cliproxyexecutor.Request{Model: model},
		cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: 50}},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	rawDecision, exists := ginCtx.Get(GinBillingDecisionKey)
	if !exists {
		t.Fatalf("expected billing decision in gin context")
	}
	decision, ok := rawDecision.(map[string]string)
	if !ok {
		t.Fatalf("billing decision type = %T, want map[string]string", rawDecision)
	}
	if got := decision["billing_class"]; got != "metered" {
		t.Fatalf("billing_class = %q, want %q", got, "metered")
	}
	reason := decision["reason"]
	for _, want := range []string{
		"threshold_rule",
		"pattern=test-*",
		"estimated_tokens=50",
		"target=metered",
		"provider=claude",
		"auth=" + meteredID,
		"selected_billing_class=metered",
	} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want substring %q", reason, want)
		}
	}
}

func TestManagerExecute_ThresholdRouting_OpusBoundarySelectsBillingClass(t *testing.T) {
	t.Parallel()

	model := "claude-3-opus"
	for _, tc := range []struct {
		name            string
		tokens          int
		wantAuth        string
		meteredPriority string
		perReqPriority  string
	}{
		{
			name:            "1500 goes to metered",
			tokens:          1500,
			wantAuth:        "metered-auth",
			meteredPriority: "1",
			perReqPriority:  "100",
		},
		{
			name:            "1501 goes to per-request",
			tokens:          1501,
			wantAuth:        "per-request-auth",
			meteredPriority: "100",
			perReqPriority:  "1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.SetConfig(&internalconfig.Config{
				Routing: internalconfig.RoutingConfig{
					TokenThresholdRules: []internalconfig.TokenThresholdRule{
						{ModelPattern: "*opus*", MaxTokens: 1500, BillingClass: internalconfig.BillingClassMetered, Enabled: true},
						{ModelPattern: "*opus*", MinTokens: 1501, BillingClass: internalconfig.BillingClassPerRequest, Enabled: true},
					},
				},
			})
			executor := &priorityFallbackExecutor{id: "claude", failAuthID: map[string]struct{}{}}
			manager.RegisterExecutor(executor)

			baseID := uuid.NewString()
			meteredID := baseID + "-metered-auth"
			perRequestID := baseID + "-per-request-auth"

			reg := registry.GetGlobalRegistry()
			for _, auth := range []*Auth{
				{ID: meteredID, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"billing_class": "metered", "priority": tc.meteredPriority}},
				{ID: perRequestID, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"billing_class": "per-request", "priority": tc.perReqPriority}},
			} {
				reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatalf("register %s: %v", auth.ID, errRegister)
				}
			}

			_, errExecute := manager.Execute(
				context.Background(),
				[]string{"claude"},
				cliproxyexecutor.Request{Model: model},
				cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: tc.tokens}},
			)
			if errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			order := executor.CallOrder()
			wantID := baseID + "-" + tc.wantAuth
			if len(order) != 1 || order[0] != wantID {
				t.Fatalf("expected %s to be used, got %v", wantID, order)
			}
		})
	}
}

func TestManagerExecute_ThresholdRouting_ClaudeAPIKeyAliasSupportsUpstreamRouteModel(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		ClaudeKey: []internalconfig.ClaudeKey{{
			APIKey:       "config-metered-key",
			BaseURL:      "https://api.apertis.ai",
			BillingClass: internalconfig.BillingClassMetered,
			Models: []internalconfig.ClaudeModel{{
				Name:  "code:claude-opus-4-6",
				Alias: "opus",
			}},
		}},
		Routing: internalconfig.RoutingConfig{
			TokenThresholdRules: []internalconfig.TokenThresholdRule{{
				ModelPattern: "*opus*",
				MaxTokens:    1500,
				BillingClass: internalconfig.BillingClassMetered,
				Enabled:      true,
			}},
		},
	})
	executor := &priorityFallbackExecutor{id: "claude", failAuthID: map[string]struct{}{}}
	manager.RegisterExecutor(executor)

	meteredAuth := &Auth{
		ID:       "config-metered-auth",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"billing_class": "metered",
			"priority":      "1",
			"api_key":       "config-metered-key",
			"base_url":      "https://api.apertis.ai",
			"auth_kind":     "apikey",
		},
	}
	perRequestAuth := &Auth{
		ID:       "oauth-per-request-auth",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"billing_class": "per-request",
			"priority":      "100",
			"auth_kind":     "oauth",
		},
	}

	for _, auth := range []*Auth{meteredAuth, perRequestAuth} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(meteredAuth.ID, "claude", []*registry.ModelInfo{{ID: "opus"}})
	t.Cleanup(func() { reg.UnregisterClient(meteredAuth.ID) })
	reg.RegisterClient(perRequestAuth.ID, "claude", []*registry.ModelInfo{{ID: "code:claude-opus-4-6"}})
	t.Cleanup(func() { reg.UnregisterClient(perRequestAuth.ID) })

	_, errExecute := manager.Execute(
		context.Background(),
		[]string{"claude"},
		cliproxyexecutor.Request{Model: "code:claude-opus-4-6"},
		cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: 1200}},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	order := executor.CallOrder()
	if len(order) != 1 || order[0] != meteredAuth.ID {
		t.Fatalf("expected metered API key auth to be used for upstream route model, got %v", order)
	}
}

func TestManagerExecute_ThresholdRouting_OpenAICompatPoolSupportsDirectUpstreamRouteModel(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "pool",
			Models: []internalconfig.OpenAICompatibilityModel{
				{Name: "qwen3.5-plus", Alias: "claude-opus-4.66"},
				{Name: "glm-5", Alias: "claude-opus-4.66"},
			},
		}},
		Routing: internalconfig.RoutingConfig{
			TokenThresholdRules: []internalconfig.TokenThresholdRule{{
				ModelPattern: "qwen*",
				MaxTokens:    1500,
				BillingClass: internalconfig.BillingClassMetered,
				Enabled:      true,
			}},
		},
	})
	executor := &priorityFallbackExecutor{id: "pool", failAuthID: map[string]struct{}{}}
	manager.RegisterExecutor(executor)

	meteredAuth := &Auth{
		ID:       "pool-metered-auth",
		Provider: "pool",
		Status:   StatusActive,
		Attributes: map[string]string{
			"billing_class": "metered",
			"priority":      "1",
			"api_key":       "metered-key",
			"compat_name":   "pool",
			"provider_key":  "pool",
		},
	}
	perRequestAuth := &Auth{
		ID:       "pool-per-request-auth",
		Provider: "pool",
		Status:   StatusActive,
		Attributes: map[string]string{
			"billing_class": "per-request",
			"priority":      "100",
			"api_key":       "per-request-key",
			"compat_name":   "pool",
			"provider_key":  "pool",
		},
	}

	for _, auth := range []*Auth{meteredAuth, perRequestAuth} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(meteredAuth.ID, "pool", []*registry.ModelInfo{{ID: "claude-opus-4.66"}})
	t.Cleanup(func() { reg.UnregisterClient(meteredAuth.ID) })
	reg.RegisterClient(perRequestAuth.ID, "pool", []*registry.ModelInfo{{ID: "qwen3.5-plus"}})
	t.Cleanup(func() { reg.UnregisterClient(perRequestAuth.ID) })

	_, errExecute := manager.Execute(
		context.Background(),
		[]string{"pool"},
		cliproxyexecutor.Request{Model: "qwen3.5-plus"},
		cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: 1200}},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	order := executor.CallOrder()
	if len(order) != 1 || order[0] != meteredAuth.ID {
		t.Fatalf("expected metered pooled alias auth to be used for direct upstream route model, got %v", order)
	}
}

func TestManagerExecute_ThresholdRouting_AliasBillingClassAcrossAPIKeyAndAuthFileCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		routeModel string
		tokens     int
		wantAuthID string
	}{
		{name: "upstream route prefers metered api key", routeModel: "code:claude-opus-4-6", tokens: 1200, wantAuthID: "config-metered-auth"},
		{name: "upstream route prefers per-request auth file", routeModel: "code:claude-opus-4-6", tokens: 1600, wantAuthID: "file-per-request-auth"},
		{name: "alias route prefers metered api key", routeModel: "opus", tokens: 1200, wantAuthID: "config-metered-auth"},
		{name: "alias route prefers per-request auth file", routeModel: "opus", tokens: 1600, wantAuthID: "file-per-request-auth"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.SetConfig(&internalconfig.Config{
				ClaudeKey: []internalconfig.ClaudeKey{{
					APIKey:       "config-metered-key",
					BaseURL:      "https://api.apertis.ai",
					BillingClass: internalconfig.BillingClassMetered,
					Models: []internalconfig.ClaudeModel{{
						Name:  "code:claude-opus-4-6",
						Alias: "opus",
					}},
				}},
				Routing: internalconfig.RoutingConfig{
					TokenThresholdRules: []internalconfig.TokenThresholdRule{
						{ModelPattern: "*opus*", MaxTokens: 1500, BillingClass: internalconfig.BillingClassMetered, Enabled: true},
						{ModelPattern: "*opus*", MinTokens: 1501, BillingClass: internalconfig.BillingClassPerRequest, Enabled: true},
					},
				},
			})
			manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
				"claude": {{Name: "code:claude-opus-4-6", Alias: "opus"}},
			})
			executor := &priorityFallbackExecutor{id: "claude", failAuthID: map[string]struct{}{}}
			manager.RegisterExecutor(executor)

			meteredAuth := &Auth{
				ID:       "config-metered-auth",
				Provider: "claude",
				Status:   StatusActive,
				Attributes: map[string]string{
					"billing_class": "metered",
					"priority":      "100",
					"api_key":       "config-metered-key",
					"base_url":      "https://api.apertis.ai",
					"auth_kind":     "apikey",
				},
			}
			fileOAuthAuth := &Auth{
				ID:       "file-per-request-auth",
				Provider: "claude",
				Status:   StatusActive,
				Attributes: map[string]string{
					"billing_class": "per-request",
					"priority":      "1",
					"auth_kind":     "oauth",
					"source":        "/tmp/claude-oauth.json",
					"path":          "/tmp/claude-oauth.json",
				},
			}

			for _, auth := range []*Auth{meteredAuth, fileOAuthAuth} {
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatalf("register %s: %v", auth.ID, errRegister)
				}
			}

			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(meteredAuth.ID, "claude", []*registry.ModelInfo{{ID: "opus"}})
			t.Cleanup(func() { reg.UnregisterClient(meteredAuth.ID) })
			reg.RegisterClient(fileOAuthAuth.ID, "claude", []*registry.ModelInfo{{ID: "code:claude-opus-4-6"}})
			t.Cleanup(func() { reg.UnregisterClient(fileOAuthAuth.ID) })

			_, errExecute := manager.Execute(
				context.Background(),
				[]string{"claude"},
				cliproxyexecutor.Request{Model: tc.routeModel},
				cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: tc.tokens}},
			)
			if errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			order := executor.CallOrder()
			if len(order) != 1 || order[0] != tc.wantAuthID {
				t.Fatalf("expected %s to be used, got %v", tc.wantAuthID, order)
			}
		})
	}
}

func TestConfigModelAliasKeysMatchingUpstream_DeduplicatesSharedAliasPool(t *testing.T) {
	t.Parallel()

	models := []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: "claude-opus-4.66"},
		{Name: "glm-5", Alias: "claude-opus-4.66"},
		{Name: "kimi-k2.5", Alias: "claude-opus-4.66"},
	}

	got := configModelAliasKeysMatchingUpstream(models, "qwen3.5-plus", "glm-5")
	want := []string{"claude-opus-4.66"}
	if len(got) != len(want) {
		t.Fatalf("alias keys len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("alias key[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConfigModelAliasKeysMatchingUpstream_ReturnsAllAliasesForSameUpstream(t *testing.T) {
	t.Parallel()

	models := []internalconfig.GeminiModel{
		{Name: "gemini-2.5-pro-exp-03-25", Alias: "g25p"},
		{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-pro"},
	}

	got := configModelAliasKeysMatchingUpstream(models, "gemini-2.5-pro-exp-03-25")
	want := []string{"g25p", "gemini-pro"}
	if len(got) != len(want) {
		t.Fatalf("alias keys len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("alias key[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMatchTokenThresholdRule_SupportsUpperLowerAndBounded(t *testing.T) {
	t.Parallel()

	rules := []internalconfig.TokenThresholdRule{
		{ModelPattern: "upper-*", MaxTokens: 100, BillingClass: internalconfig.BillingClassMetered, Enabled: true},
		{ModelPattern: "lower-*", MinTokens: 101, BillingClass: internalconfig.BillingClassPerRequest, Enabled: true},
		{ModelPattern: "bounded-*", MinTokens: 10, MaxTokens: 20, BillingClass: internalconfig.BillingClassMetered, Enabled: true},
	}

	tests := []struct {
		name      string
		model     string
		count     int
		want      internalconfig.BillingClass
		wantMatch bool
	}{
		{name: "upper only exact", model: "upper-opus", count: 100, want: internalconfig.BillingClassMetered, wantMatch: true},
		{name: "lower only exact", model: "lower-opus", count: 101, want: internalconfig.BillingClassPerRequest, wantMatch: true},
		{name: "bounded middle", model: "bounded-opus", count: 15, want: internalconfig.BillingClassMetered, wantMatch: true},
		{name: "bounded over max", model: "bounded-opus", count: 21, wantMatch: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rule, ok := matchTokenThresholdRule(rules, tc.model, tc.count)
			if ok != tc.wantMatch {
				t.Fatalf("matchTokenThresholdRule() match = %v, want %v", ok, tc.wantMatch)
			}
			if !tc.wantMatch {
				return
			}
			if rule.BillingClass != tc.want {
				t.Fatalf("billing class = %q, want %q", rule.BillingClass, tc.want)
			}
		})
	}
}

func TestManagerExecute_ThresholdRouting_MissingTokensOrModelMismatchKeepsExistingBehavior(t *testing.T) {
	t.Parallel()

	model := "claude-3-opus"
	setupAuths := func(t *testing.T) (*Manager, *priorityFallbackExecutor, string) {
		t.Helper()
		manager := NewManager(nil, &RoundRobinSelector{}, nil)
		manager.SetRetryConfig(0, 0, 0)
		manager.SetConfig(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{
				TokenThresholdRules: []internalconfig.TokenThresholdRule{{
					ModelPattern: "*opus*",
					MaxTokens:    1500,
					BillingClass: internalconfig.BillingClassMetered,
					Enabled:      true,
				}},
			},
		})
		executor := &priorityFallbackExecutor{id: "claude", failAuthID: map[string]struct{}{}}
		manager.RegisterExecutor(executor)

		baseID := uuid.NewString()
		reg := registry.GetGlobalRegistry()
		for _, auth := range []*Auth{
			{ID: baseID + "-metered-auth", Provider: "claude", Status: StatusActive, Attributes: map[string]string{"billing_class": "metered", "priority": "100"}},
			{ID: baseID + "-per-request-auth", Provider: "claude", Status: StatusActive, Attributes: map[string]string{"billing_class": "per-request", "priority": "1"}},
		} {
			reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}, {ID: "claude-3-sonnet"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register %s: %v", auth.ID, errRegister)
			}
		}
		return manager, executor, baseID
	}

	t.Run("missing estimated tokens", func(t *testing.T) {
		manager, executor, baseID := setupAuths(t)
		_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
		order := executor.CallOrder()
		if len(order) != 1 || order[0] != baseID+"-metered-auth" {
			t.Fatalf("expected default priority auth, got %v", order)
		}
	})

	t.Run("model mismatch", func(t *testing.T) {
		manager, executor, baseID := setupAuths(t)
		_, errExecute := manager.Execute(
			context.Background(),
			[]string{"claude"},
			cliproxyexecutor.Request{Model: "claude-3-sonnet"},
			cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: 1200}},
		)
		if errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
		order := executor.CallOrder()
		if len(order) != 1 || order[0] != baseID+"-metered-auth" {
			t.Fatalf("expected default priority auth on model mismatch, got %v", order)
		}
	})
}

func TestAuthBillingClass_RecognizesAttributeAndMetadataVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		auth *Auth
		want string
	}{
		{name: "attribute snake case", auth: &Auth{Attributes: map[string]string{"billing_class": "metered"}}, want: "metered"},
		{name: "attribute kebab case", auth: &Auth{Attributes: map[string]string{"billing-class": "per-request"}}, want: "per-request"},
		{name: "metadata snake case", auth: &Auth{Metadata: map[string]any{"billing_class": "per_request"}}, want: "per-request"},
		{name: "metadata kebab case", auth: &Auth{Metadata: map[string]any{"billing-class": "metered"}}, want: "metered"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := authBillingClass(tc.auth); got != tc.want {
				t.Fatalf("authBillingClass() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestManager_ModelSupportBadRequest_FallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    "invalid_request_error: The requested model is not supported.",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	request := cliproxyexecutor.Request{Model: model}
	for i := 0; i < 2; i++ {
		resp, errExecute := m.Execute(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("execute %d error = %v, want success", i, errExecute)
		}
		if string(resp.Payload) != goodAuth.ID {
			t.Fatalf("execute %d payload = %q, want %q", i, string(resp.Payload), goodAuth.ID)
		}
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID, goodAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected bad auth model state to be unavailable")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected bad auth model state cooldown to be set")
	}
}

func TestManagerExecuteStream_ModelSupportBadRequestFallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		streamFirstErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    "invalid_request_error: The requested model is not supported.",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	request := cliproxyexecutor.Request{Model: model}
	for i := 0; i < 2; i++ {
		streamResult, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("execute stream %d error = %v, want success", i, errExecute)
		}
		var payload []byte
		for chunk := range streamResult.Chunks {
			if chunk.Err != nil {
				t.Fatalf("execute stream %d chunk error = %v, want success", i, chunk.Err)
			}
			payload = append(payload, chunk.Payload...)
		}
		if string(payload) != goodAuth.ID {
			t.Fatalf("execute stream %d payload = %q, want %q", i, string(payload), goodAuth.ID)
		}
	}

	got := executor.StreamCalls()
	want := []string{badAuth.ID, goodAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected bad auth model state to be unavailable")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected bad auth model state cooldown to be set")
	}
}

func TestManager_MarkResult_RespectsAuthDisableCoolingOverride(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model"
	m.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: 500, Message: "boom"},
	})

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}
}

func TestManager_MarkResult_RequestScopedNotFoundDoesNotCooldownAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "openai",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-4.1"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    requestScopedNotFoundMessage,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Unavailable {
		t.Fatalf("expected request-scoped 404 to keep auth available")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected request-scoped 404 to keep auth cooldown unset, got %v", updated.NextRetryAfter)
	}
	if state := updated.ModelStates[model]; state != nil {
		t.Fatalf("expected request-scoped 404 to avoid model cooldown state, got %#v", state)
	}
}

func TestManager_RequestScopedNotFoundStopsRetryWithoutSuspendingAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "openai",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusNotFound,
				Message:    requestScopedNotFoundMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "gpt-4.1"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "openai"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "openai"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	_, errExecute := m.Execute(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected request-scoped not-found error")
	}
	errResult, ok := errExecute.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", errExecute)
	}
	if errResult.HTTPStatus != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", errResult.HTTPStatus, http.StatusNotFound)
	}
	if errResult.Message != requestScopedNotFoundMessage {
		t.Fatalf("message = %q, want %q", errResult.Message, requestScopedNotFoundMessage)
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	if updatedBad.Unavailable {
		t.Fatalf("expected request-scoped 404 to keep bad auth available")
	}
	if !updatedBad.NextRetryAfter.IsZero() {
		t.Fatalf("expected request-scoped 404 to keep bad auth cooldown unset, got %v", updatedBad.NextRetryAfter)
	}
	if state := updatedBad.ModelStates[model]; state != nil {
		t.Fatalf("expected request-scoped 404 to avoid bad auth model cooldown state, got %#v", state)
	}
}

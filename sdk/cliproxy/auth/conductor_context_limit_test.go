package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestRoutingContextLimitForModels(t *testing.T) {
	limited := func(name, alias string, limit int) modelAliasEntry {
		return internalconfig.OpenAICompatibilityModel{Name: name, Alias: alias, RoutingMaxContextLength: limit}
	}
	tests := []struct {
		name      string
		requested string
		models    []modelAliasEntry
		want      int
	}{
		{"alias match carries its limit", "aa", []modelAliasEntry{limited("ds", "aa", 320000)}, 320000},
		{"thinking suffix still matches the alias", "aa(8192)", []modelAliasEntry{limited("ds", "aa", 320000)}, 320000},
		{"unlimited pooled upstream keeps the auth unlimited", "aa", []modelAliasEntry{limited("ds", "aa", 320000), limited("ds-1m", "aa", 0)}, 0},
		{"largest pooled limit wins", "aa", []modelAliasEntry{limited("ds", "aa", 320000), limited("ds-big", "aa", 640000)}, 640000},
		{"direct name match when no alias matches", "ds", []modelAliasEntry{limited("ds", "aa", 320000)}, 320000},
		{"alias match outranks a name match", "aa", []modelAliasEntry{limited("aa", "", 100000), limited("ds", "aa", 320000)}, 320000},
		{"unrelated model is unlimited", "other", []modelAliasEntry{limited("ds", "aa", 320000)}, 0},
		{"advertised max-context-length is not a routing limit", "aa", []modelAliasEntry{internalconfig.OpenAICompatibilityModel{Name: "ds", Alias: "aa", MaxContextLength: 320000}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := routingContextLimitForModels(tc.requested, tc.models); got != tc.want {
				t.Fatalf("routingContextLimitForModels(%q) = %d, want %d", tc.requested, got, tc.want)
			}
		})
	}
}

// newRoutingContextLimitManager builds two OpenAI-compatible providers that
// serve the same alias: "small" caps routed input at 300000 tokens, "big" has
// no cap. Each provider has one config-backed API-key auth.
func newRoutingContextLimitManager(t *testing.T, selector Selector, withBig bool) (*Manager, *authScopedOpenAICompatPoolExecutor) {
	t.Helper()
	const alias = "aa"
	compat := []internalconfig.OpenAICompatibility{{
		Name:   "small",
		Models: []internalconfig.OpenAICompatibilityModel{{Name: "ds-small", Alias: alias, RoutingMaxContextLength: 300000}},
	}}
	if withBig {
		compat = append(compat, internalconfig.OpenAICompatibility{
			Name:   "big",
			Models: []internalconfig.OpenAICompatibilityModel{{Name: "ds-big", Alias: alias}},
		})
	}
	m := NewManager(nil, selector, nil)
	m.SetConfig(&internalconfig.Config{OpenAICompatibility: compat})
	executor := &authScopedOpenAICompatPoolExecutor{id: openAICompatPoolProviderKey}
	m.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	for _, entry := range compat {
		auth := &Auth{
			ID:       entry.Name + "-auth",
			Provider: openAICompatPoolProviderKey,
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      entry.Name + "-key",
				"compat_name":  entry.Name,
				"provider_key": openAICompatPoolProviderKey,
			},
		}
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		reg.RegisterClient(auth.ID, openAICompatPoolProviderKey, []*registry.ModelInfo{{ID: alias}})
		authID := auth.ID
		t.Cleanup(func() { reg.UnregisterClient(authID) })
	}
	return m, executor
}

func routingContextOpts(tokens int) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: tokens}}
}

func executedAuthPrefixes(calls []string) map[string]int {
	out := make(map[string]int)
	for _, call := range calls {
		authID, _, _ := strings.Cut(call, "|")
		prefix, _, _ := strings.Cut(authID, "-auth")
		out[prefix]++
	}
	return out
}

// The weight-robin selector is the strategy cpa-152 runs with: requests over
// the small provider's limit stay on the big one, smaller requests keep
// rotating across both.
func TestExecuteRoutingContextLimitSplitsSharedAlias(t *testing.T) {
	m, executor := newRoutingContextLimitManager(t, &WeightedRobinSelector{}, true)

	for i := 0; i < 6; i++ {
		if _, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "aa"}, routingContextOpts(350000)); err != nil {
			t.Fatalf("execute over the limit: %v", err)
		}
	}
	over := executedAuthPrefixes(executor.ExecuteCalls())
	if over["small"] != 0 || over["big"] != 6 {
		t.Fatalf("requests over 300000 tokens went to %v, want all 6 on big", over)
	}

	for i := 0; i < 6; i++ {
		if _, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "aa"}, routingContextOpts(1000)); err != nil {
			t.Fatalf("execute under the limit: %v", err)
		}
	}
	all := executedAuthPrefixes(executor.ExecuteCalls())
	if all["small"] == 0 || all["big"] <= 6 {
		t.Fatalf("requests under the limit must rotate across both providers, got %v", all)
	}
}

func TestExecuteRoutingContextLimitWithoutEstimateKeepsEveryProvider(t *testing.T) {
	m, executor := newRoutingContextLimitManager(t, &WeightedRobinSelector{}, true)
	for i := 0; i < 6; i++ {
		if _, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "aa"}, cliproxyexecutor.Options{}); err != nil {
			t.Fatalf("execute without estimate: %v", err)
		}
	}
	got := executedAuthPrefixes(executor.ExecuteCalls())
	if got["small"] == 0 || got["big"] == 0 {
		t.Fatalf("requests without a token estimate must not be filtered, got %v", got)
	}
}

func TestExecuteRoutingContextLimitExhaustedReportsEstimate(t *testing.T) {
	m, executor := newRoutingContextLimitManager(t, &WeightedRobinSelector{}, false)
	_, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "aa"}, routingContextOpts(350000))
	if err == nil {
		t.Fatal("execute must fail when every provider is below the request size")
	}
	if !strings.Contains(err.Error(), "auth_not_found") || !strings.Contains(err.Error(), "350000") {
		t.Fatalf("error = %v, want auth_not_found naming the 350000-token estimate", err)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 0 {
		t.Fatalf("no upstream call may be made, got %v", calls)
	}
}

// One provider pooling two upstream models under the same alias: the auth stays
// eligible through the big member, and execution must not rotate a large
// request onto the small one.
func TestExecuteRoutingContextLimitFiltersAliasPoolMembers(t *testing.T) {
	executor := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey}
	m := newOpenAICompatPoolTestManager(t, "aa", []internalconfig.OpenAICompatibilityModel{
		{Name: "ds-small", Alias: "aa", RoutingMaxContextLength: 300000},
		{Name: "ds-big", Alias: "aa"},
	}, executor)

	for i := 0; i < 4; i++ {
		if _, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "aa"}, routingContextOpts(350000)); err != nil {
			t.Fatalf("execute over the small member's limit: %v", err)
		}
	}
	for _, model := range executor.ExecuteModels() {
		if model != "ds-big" {
			t.Fatalf("large requests reached %q, want only ds-big; calls %v", model, executor.ExecuteModels())
		}
	}

	for i := 0; i < 4; i++ {
		if _, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "aa"}, routingContextOpts(1000)); err != nil {
			t.Fatalf("execute under the limit: %v", err)
		}
	}
	seen := map[string]bool{}
	for _, model := range executor.ExecuteModels()[4:] {
		seen[model] = true
	}
	if !seen["ds-small"] || !seen["ds-big"] {
		t.Fatalf("small requests must keep rotating through the whole pool, got %v", executor.ExecuteModels()[4:])
	}
}

func TestFilterExecutionModelsByRoutingContextMergesDuplicatesAndSuffixes(t *testing.T) {
	m := newOpenAICompatPoolTestManager(t, "aa", []internalconfig.OpenAICompatibilityModel{
		{Name: "dup", Alias: "aa", RoutingMaxContextLength: 300000},
		{Name: "dup", Alias: "aa"},
		{Name: "capped", Alias: "aa", RoutingMaxContextLength: 300000},
		{Name: "capped", Alias: "aa", RoutingMaxContextLength: 200000},
		{Name: "suffixed(8192)", Alias: "aa", RoutingMaxContextLength: 300000},
	}, nil)
	auth, ok := m.GetByID("pool-auth-" + t.Name())
	if !ok {
		t.Fatal("pool auth not registered")
	}
	got := m.filterExecutionModelsByRoutingContext(auth, "aa", []string{"dup", "capped", "suffixed(8192)"}, routingContextOpts(350000), nil)
	if len(got) != 1 || got[0] != "dup" {
		t.Fatalf("filtered models = %v, want [dup]: an unlimited duplicate keeps dup, the largest duplicate limit caps capped, and a suffixed name still carries its limit", got)
	}
	got = m.filterExecutionModelsByRoutingContext(auth, "aa", []string{"dup", "capped", "suffixed(8192)"}, routingContextOpts(250000), nil)
	if len(got) != 3 {
		t.Fatalf("filtered models = %v, want all three under every limit", got)
	}
}

func TestPickNextMixedFastPathHonorsRoutingContextLimit(t *testing.T) {
	const model = "deepseek-v4.1-flash"
	manager := NewManager(nil, nil, nil)
	manager.executors["ollama-openaicompatible"] = schedulerTestExecutor{}
	manager.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{
		{Name: "small", Models: []internalconfig.OpenAICompatibilityModel{{Name: model, RoutingMaxContextLength: 300000}}},
		{Name: "big", Models: []internalconfig.OpenAICompatibilityModel{{Name: model}}},
	}})
	for _, auth := range []*Auth{
		{ID: "small-auth", Provider: "openai-compatibility", Attributes: map[string]string{"api_key": "small-key", "compat_name": "small", "provider_key": "ollama-openaicompatible"}},
		{ID: "big-auth", Provider: "openai-compatibility", Attributes: map[string]string{"api_key": "big-key", "compat_name": "big", "provider_key": "ollama-openaicompatible"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	registerSchedulerModels(t, "ollama-openaicompatible", model, "small-auth", "big-auth")
	if !manager.useSchedulerFastPath() {
		t.Skip("scheduler fast path unavailable without built-in selector state")
	}

	for i := 0; i < 4; i++ {
		selected, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"ollama-openaicompatible"}, model, routingContextOpts(350000), nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() error = %v", errPick)
		}
		if selected == nil || selected.ID != "big-auth" {
			t.Fatalf("pickNextMixed() selected %v, want big-auth for a request over the small provider's limit", selected)
		}
	}
}

// Native providers (claude, gemini, codex, ...) have no
// routing-max-context-length, so the catalog window must be the ceiling that
// keeps an oversized prompt from reaching the upstream.
func TestRegisteredModelContextWindow(t *testing.T) {
	tests := []struct {
		name  string
		auth  *Auth
		model string
		want  int
	}{
		{"1M catalog entry", &Auth{Provider: "claude"}, "claude-sonnet-5-5", 1000000},
		{"200K catalog entry", &Auth{Provider: "claude"}, "claude-haiku-4-5-20251001", 200000},
		{"thinking suffix still resolves", &Auth{Provider: "claude"}, "claude-sonnet-5-5(8192)", 1000000},
		{"unknown model has no ceiling", &Auth{Provider: "claude"}, "claude-nonexistent-9", 0},
		{"another family's catalog is not borrowed", &Auth{Provider: "openai-compatibility"}, "claude-sonnet-5-5", 0},
		{"nil auth has no ceiling", nil, "claude-sonnet-5-5", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := registeredModelContextWindow(tc.auth, tc.model); got != tc.want {
				t.Fatalf("registeredModelContextWindow(%v, %q) = %d, want %d", tc.auth, tc.model, got, tc.want)
			}
		})
	}
}

func TestAuthExceedsRoutingContextLimitUsesCatalogWindow(t *testing.T) {
	m := NewManager(nil, nil, nil)
	claudeAuth := &Auth{ID: "claude-window-auth", Provider: "claude", Status: StatusActive}

	tokens, limit, exceeded := m.authExceedsRoutingContextLimit(claudeAuth, "claude-sonnet-5-5", routingContextOpts(1104478))
	if !exceeded || limit != 1000000 || tokens != 1104478 {
		t.Fatalf("authExceedsRoutingContextLimit() = (%d, %d, %t), want (1104478, 1000000, true)", tokens, limit, exceeded)
	}
	if _, limit, exceeded := m.authExceedsRoutingContextLimit(claudeAuth, "claude-sonnet-5-5", routingContextOpts(990000)); exceeded {
		t.Fatalf("a prompt under the %d-token window must stay eligible", limit)
	}
	if _, _, exceeded := m.authExceedsRoutingContextLimit(claudeAuth, "claude-nonexistent-9", routingContextOpts(5000000)); exceeded {
		t.Fatal("a model without a known window must not be excluded")
	}
	if _, _, exceeded := m.authExceedsRoutingContextLimit(claudeAuth, "claude-sonnet-5-5", cliproxyexecutor.Options{}); exceeded {
		t.Fatal("a request without a token estimate must not be excluded")
	}
}

// The incident behind this guard: a 1,104,478-token claude prompt hit
// "prompt is too long: ... > 1000000 maximum" on one credential and the same
// body was then uploaded to the next one.
func TestExecuteRejectsPromptOverCatalogWindowWithoutUpstreamCall(t *testing.T) {
	executor := &claudeCancellationTestExecutor{}
	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID:         "claude-window-exec-auth-" + uuid.NewString(),
		Provider:   "claude",
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"request_retry": float64(0),
		},
	}
	model := "claude-sonnet-5-5"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, routingContextOpts(1104478))
	if errExecute == nil {
		t.Fatal("an oversized prompt must fail before execution")
	}
	if !strings.Contains(errExecute.Error(), "auth_not_found") || !strings.Contains(errExecute.Error(), "1104478") {
		t.Fatalf("error = %v, want auth_not_found naming the 1104478-token estimate", errExecute)
	}
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("upstream executions = %d, want 0: the prompt cannot fit the model window", got)
	}
	requireClaudeCancellationNeutral(t, manager, auth.ID, model)

	if _, errSmall := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, routingContextOpts(990000)); errSmall != nil {
		t.Fatalf("a prompt under the window must execute, got %v", errSmall)
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("upstream executions = %d, want 1 for the in-window prompt", got)
	}
}

func TestIsContextOverflowError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "anthropic prompt too long body",
			err:  &Error{Code: "invalid_request_error", Message: `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1104478 tokens > 1000000 maximum"}}`, HTTPStatus: 400},
			want: true,
		},
		{name: "code only", err: &Error{Code: "context_length_exceeded", Message: "input too large", HTTPStatus: 400}, want: true},
		{name: "openai style message", err: &Error{Message: "This model's maximum context length is 200000 tokens", HTTPStatus: 400}, want: true},
		{name: "ordinary 400", err: &Error{Code: "invalid_request_error", Message: "messages: at least one message is required", HTTPStatus: 400}, want: false},
		{name: "rate limit", err: &Error{Code: "rate_limit_exceeded", Message: "slow down", HTTPStatus: 429}, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContextOverflowError(tc.err); got != tc.want {
				t.Fatalf("isContextOverflowError(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

// Rotation must not repeat an oversized upload on the next credential, while
// ordinary 400s keep flowing through the credential/model fallback chains.
func TestIsRequestInvalidErrorStopsOnPromptTooLong(t *testing.T) {
	overflow := &Error{
		Code:       "invalid_request_error",
		Message:    `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1110175 tokens > 1000000 maximum"}}`,
		HTTPStatus: http.StatusBadRequest,
	}
	if !isRequestInvalidError(overflow) {
		t.Fatal("an oversized prompt must stop credential rotation")
	}
	ordinary := &Error{Code: "invalid_request_error", Message: "messages: at least one message is required", HTTPStatus: http.StatusBadRequest}
	if isRequestInvalidError(ordinary) {
		t.Fatal("an ordinary 400 must keep falling through to the fallback chains")
	}
}

// The incident: the first credential rejected the prompt as too long and the
// same 1.1M-token body was uploaded to the next credential. An overflow must
// stop on the first credential, even when rotation is enabled.
func TestExecutePromptTooLongDoesNotRotateCredentials(t *testing.T) {
	executor := &claudeCancellationTestExecutor{}
	overflow := &Error{
		Code:       "invalid_request_error",
		Message:    `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1110175 tokens > 1000000 maximum"}}`,
		HTTPStatus: http.StatusBadRequest,
	}
	executor.executeFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
		return cliproxyexecutor.Response{}, overflow
	}
	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(3, 3, 0)
	manager.RegisterExecutor(executor)
	model := "claude-sonnet-5-5"
	for i := 0; i < 2; i++ {
		auth := &Auth{
			ID:         "claude-overflow-auth-" + uuid.NewString(),
			Provider:   "claude",
			Attributes: map[string]string{"auth_kind": "oauth"},
			Metadata: map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"request_retry": float64(3),
			},
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		authID := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
	}

	// Keep the estimate under the window so the preflight gate stays out of the
	// way and the upstream rejection is what has to stop the rotation.
	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, routingContextOpts(1000))
	if errExecute == nil {
		t.Fatal("the upstream rejection must reach the caller")
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("upstream executions = %d, want 1: an oversized prompt must not be replayed on another credential", got)
	}
}

// A claude credential has no routing-max-context-length, so the catalog window is
// its ceiling. When the request exceeds it, the refusal must reach the client as
// a 400 it can act on, not as "no auth available" (which surfaces as 500 and
// reads like a proxy fault).
func TestOversizedNativeProviderRequestCarriesClientError(t *testing.T) {
	exec := &contextCeilingTestExecutor{}
	const model = "claude-sonnet-5-5"
	manager := contextCeilingTestManager(t, exec, model, 1000000)

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, routingContextOpts(1104478))
	if errExecute == nil {
		t.Fatal("a 1104478-token prompt must be refused against the 1000000-token window")
	}
	if exec.calls != 0 {
		t.Fatalf("no upstream call may be made, got %d", exec.calls)
	}
	var authErr *Error
	if !errors.As(errExecute, &authErr) || authErr == nil {
		t.Fatalf("error = %T %v, want an *Error", errExecute, errExecute)
	}
	// The code stays auth_not_found so a route with a larger-window fallback
	// model still gets its chain, but the status must be 400: an unset status
	// becomes 503, which tells the client to retry a prompt that cannot fit and
	// sends the operator looking at credential cooldown instead of the request.
	if authErr.Code != "auth_not_found" {
		t.Fatalf("code = %q, want auth_not_found so fallback chains still run", authErr.Code)
	}
	if status := clienterror.HTTPStatusFromError(errExecute); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 rather than the 503 an unset status produces", status)
	}
	if !strings.Contains(authErr.Message, "1104478") || !strings.Contains(authErr.Message, "1000000") {
		t.Fatalf("message = %q, want both the estimate and the window named", authErr.Message)
	}
}

// The context-1m beta raises the window the upstream accepts for a 200K model, so
// the selection gate must not refuse a prompt the beta unlocks.
func TestContext1MBetaRaisesNativeProviderCeiling(t *testing.T) {
	exec := &contextCeilingTestExecutor{}
	const model = "claude-sonnet-4-5-20250929"
	manager := contextCeilingTestManager(t, exec, model, 200000)

	if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, routingContextOpts(900000)); errExecute == nil {
		t.Fatal("without the beta header a 900000-token prompt exceeds the 200000-token window")
	}

	beta := routingContextOpts(900000)
	beta.Headers = http.Header{"Anthropic-Beta": []string{"context-1m-2025-08-07"}}
	if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, beta); errExecute != nil {
		t.Fatalf("the context-1m beta must allow a 900000-token prompt, got %v", errExecute)
	}
	if exec.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", exec.calls)
	}
}

// A request under the window is untouched, and an unknown model keeps routing.
func TestContextCeilingLeavesNormalRequestsAlone(t *testing.T) {
	exec := &contextCeilingTestExecutor{}
	const model = "claude-sonnet-5-5"
	manager := contextCeilingTestManager(t, exec, model, 1000000)

	for _, tokens := range []int{1000, 1000000} {
		if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, routingContextOpts(tokens)); errExecute != nil {
			t.Fatalf("a %d-token prompt must be attempted, got %v", tokens, errExecute)
		}
	}
	if exec.calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", exec.calls)
	}

	unknown := contextCeilingTestManager(t, &contextCeilingTestExecutor{}, "model-without-a-catalog-entry", 0)
	if _, errExecute := unknown.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "model-without-a-catalog-entry"}, routingContextOpts(5000000)); errExecute != nil {
		t.Fatalf("an unknown model has no ceiling and must keep routing, got %v", errExecute)
	}
}

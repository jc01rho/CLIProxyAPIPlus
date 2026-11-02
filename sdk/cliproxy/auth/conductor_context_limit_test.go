package auth

import (
	"context"
	"strings"
	"testing"

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

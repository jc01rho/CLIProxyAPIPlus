package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerWeightedAliasesFallbackOnlyAfterRealQuotaFailure(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, shape := range []string{"oauth-glm", "claude-key-opus"} {
		for _, path := range []string{"execute", "stream"} {
			t.Run(shape+"/"+path, func(t *testing.T) {
				f := newAliasAvailabilityFixture(t, &WeightedRobinSelector{}, shape, false)
				fallback := &Auth{ID: "fallback-" + t.Name(), Provider: "synthetic-fallback", Status: StatusActive}
				fallbackExecutor := &aliasModelPoolExecutor{provider: fallback.Provider}
				f.manager.RegisterExecutor(fallbackExecutor)
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(fallback.ID, fallback.Provider, []*registry.ModelInfo{{ID: "higher-coding"}})
				t.Cleanup(func() { reg.UnregisterClient(fallback.ID) })
				if _, err := f.manager.Register(context.Background(), fallback); err != nil {
					t.Fatal(err)
				}
				if f.route == "opus" {
					f.manager.SetFallbackModels(map[string]string{"opus": "higher-coding"})
				} else {
					f.manager.SetFallbackChain([]string{"higher-coding"}, 5)
				}
				want := f.auth.ID + ":" + f.targets[0]
				if got, err := f.run(t, path, "unwrapped"); err != nil || got != want {
					t.Fatalf("healthy deployed route shape: payload=%q error=%v, want %q", got, err, want)
				}
				if got := len(fallbackExecutor.ExecuteCalls()) + len(fallbackExecutor.StreamCalls()); got != 0 {
					t.Fatalf("healthy alias unnecessarily fell back: %d fallback calls", got)
				}
				f.executor.mu.Lock()
				f.executor.failFor = map[string]error{f.targets[0]: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "real synthetic upstream quota"}}
				f.executor.mu.Unlock()
				for attempt := 0; attempt < 2; attempt++ {
					want = fallback.ID + ":higher-coding"
					if got, err := f.run(t, path, "unwrapped"); err != nil || got != want {
						t.Fatalf("quota fallback attempt %d: payload=%q error=%v, want %q", attempt, got, err, want)
					}
				}
				if got := len(f.executor.ExecuteCalls()) + len(f.executor.StreamCalls()); got != 2 {
					t.Fatalf("primary calls=%d, want healthy call then real 429; later request must retain cooldown", got)
				}
			})
		}
	}
}

func TestManagerWeightedAliasPreservesDisabledAndKindFiltering(t *testing.T) {
	withQuotaCooldownEnabled(t)
	f := newAliasAvailabilityFixture(t, &WeightedRobinSelector{}, "oauth-glm", true)
	ctx := context.Background()
	if selected, err := f.manager.SelectAuthByKind(ctx, f.auth.Provider, f.route, AuthKindAPIKey, cliproxyexecutor.Options{}); selected != nil || err == nil {
		t.Fatalf("ineligible OAuth auth selected as API key: selected=%v error=%v", selected, err)
	}
	disabled := f.auth.Clone()
	disabled.ID += "-disabled"
	disabled.Disabled = true
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(disabled.ID, disabled.Provider, []*registry.ModelInfo{{ID: f.route}, {ID: f.targets[0]}})
	t.Cleanup(func() { reg.UnregisterClient(disabled.ID) })
	if _, err := f.manager.Register(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	f.cool(f.stateKey, false)
	if selected, err := f.manager.SelectAuth(ctx, f.auth.Provider, f.route, cliproxyexecutor.Options{}); selected != nil || err == nil {
		t.Fatalf("disabled auth must not replace cooling target: selected=%v error=%v", selected, err)
	}
}

func TestManagerSelectAuthConfiguredAliasPoolIgnoresStaleRouteCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	ctx := context.Background()
	manager := newOpenAICompatPoolTestManager(t, "glm", []internalconfig.OpenAICompatibilityModel{
		{Name: "healthy-target-a", Alias: "glm"},
		{Name: "healthy-target-b", Alias: "glm"},
	}, nil)
	manager.SetSelector(&WeightedRobinSelector{})
	candidateID := "pool-auth-" + t.Name()
	retryAfter := time.Hour
	manager.MarkResult(ctx, Result{
		AuthID: candidateID, Provider: openAICompatPoolProviderKey, Model: "glm", RetryAfter: &retryAfter,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "previous route quota"},
	})
	stored, _ := manager.GetByID(candidateID)
	models, pooled, _, _ := manager.preparedExecutionModelsWithAlias(stored, "glm")
	if !pooled || len(models) != 2 {
		t.Fatalf("prepared execution models = %v, pooled=%v, want both healthy targets", models, pooled)
	}
	selected, err := manager.SelectAuth(ctx, openAICompatPoolProviderKey, "glm", cliproxyexecutor.Options{})
	if err != nil || selected == nil || selected.ID != candidateID {
		t.Fatalf("healthy alias pool: selected=%v error=%v, want %s", selected, err, candidateID)
	}
}

func TestManagerSelectAuthWeightedRobinUsesAliasTarget(t *testing.T) {
	withQuotaCooldownEnabled(t)
	ctx := context.Background()
	manager := NewManager(nil, &WeightedRobinSelector{}, nil)
	manager.RegisterExecutor(&aliasRoutingExecutor{id: "codex"})
	candidate := &Auth{ID: "weighted-alias-" + t.Name(), Provider: "codex", Status: StatusActive}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(candidate.ID, "codex", []*registry.ModelInfo{{ID: "opus"}, {ID: "healthy-target"}})
	t.Cleanup(func() { reg.UnregisterClient(candidate.ID) })
	if _, err := manager.Register(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	retryAfter := time.Hour
	manager.MarkResult(ctx, Result{
		AuthID: candidate.ID, Provider: "codex", Model: "opus", RetryAfter: &retryAfter,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "previous route quota"},
	})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "healthy-target", Alias: "opus", Fork: true}},
	})
	stored, _ := manager.GetByID(candidate.ID)
	if got := manager.selectionModelForAuth(stored, "opus"); got != "healthy-target" {
		t.Fatalf("selection model = %q, want healthy-target", got)
	}
	if got := manager.ResolveExecutionModel(stored, "opus"); got != "healthy-target" {
		t.Fatalf("execution model = %q, want healthy-target", got)
	}
	selected, err := manager.SelectAuth(ctx, "codex", "opus", cliproxyexecutor.Options{})
	if err != nil || selected == nil || selected.ID != candidate.ID {
		t.Fatalf("healthy alias target: selected=%v error=%v, want %s", selected, err, candidate.ID)
	}
}

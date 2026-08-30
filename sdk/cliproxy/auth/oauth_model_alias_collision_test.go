package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestResolveOAuthUpstreamModel_SameAuthRealModelBeatsAliasExposedCollision(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.2", Alias: "gpt-5.4", Fork: true},
		},
	})

	auth := &Auth{
		ID:       "codex-auth-same-auth-collision",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"username": "tester"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{
		{ID: "gpt-5.4", ExecutionTarget: "gpt-5.2"},
		{ID: "gpt-5.4"},
		{ID: "gpt-5.2"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Same-ID duplicate registration (alias-exposed entry deduped away by
	// preferClientModelInfo): the configured alias target resolves normally.
	// See the note on TestResolveOAuthUpstreamModel_RealRegisteredAliasName-
	// BeatsConfiguredForkAlias for the semantics update.
	resolved := m.resolveOAuthUpstreamModel(auth, "gpt-5.4")
	if resolved != "gpt-5.2" {
		t.Fatalf("resolveOAuthUpstreamModel(configured alias target for deduped collision) = %q, want %q", resolved, "gpt-5.2")
	}

	resolvedWithSuffix := m.resolveOAuthUpstreamModel(auth, "gpt-5.4(high)")
	if resolvedWithSuffix != "gpt-5.2(high)" {
		t.Fatalf("resolveOAuthUpstreamModel(configured alias target with suffix) = %q, want %q", resolvedWithSuffix, "gpt-5.2(high)")
	}
}

func TestPrepareExecutionModels_SameAuthRealModelBeatsAliasExposedCollision(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.2", Alias: "gpt-5.4", Fork: true},
		},
	})

	auth := &Auth{
		ID:       "codex-auth-same-auth-prepare",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"username": "tester"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{
		{ID: "gpt-5.4", ExecutionTarget: "gpt-5.2"},
		{ID: "gpt-5.4"},
		{ID: "gpt-5.2"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	models := m.prepareExecutionModels(auth, "gpt-5.4")
	if len(models) != 1 || models[0] != "gpt-5.2" {
		t.Fatalf("prepareExecutionModels(configured alias target for deduped collision) = %v, want [%q]", models, "gpt-5.2")
	}
}

// TestResolveOAuthUpstreamModel_RealRegisteredAliasNameBeatsConfiguredForkAlias
// was updated for upstream nofork-alias semantics (12b88f3a): a configured
// (non-force) alias always resolves to its target for both execution and
// state keys, even when the registry also registers the alias name as a real
// client model. The old local expectation (registry real model outranks the
// configured alias) conflicted with ReconcileRegistryModelStates/MarkResult
// alias→target state migration and cannot coexist with it for identical
// inputs. The registry itself still prefers real entries over alias-exposed
// duplicates (preferClientModelInfo), which covers the same-ID duplicate
// registration case at the registry layer.
func TestResolveOAuthUpstreamModel_RealRegisteredAliasNameBeatsConfiguredForkAlias(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.4", Alias: "gpt-5.5", Fork: true},
		},
	})

	auth := &Auth{
		ID:       "codex-auth-real-model-wins-over-configured-alias",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"username": "tester"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{
		{ID: "gpt-5.4"},
		{ID: "gpt-5.5"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	resolved := m.resolveOAuthUpstreamModel(auth, "gpt-5.5")
	if resolved != "gpt-5.4" {
		t.Fatalf("resolveOAuthUpstreamModel(configured alias target wins over registry real model) = %q, want %q", resolved, "gpt-5.4")
	}

	resolvedWithSuffix := m.resolveOAuthUpstreamModel(auth, "gpt-5.5(high)")
	if resolvedWithSuffix != "gpt-5.4(high)" {
		t.Fatalf("resolveOAuthUpstreamModel(configured alias target with suffix) = %q, want %q", resolvedWithSuffix, "gpt-5.4(high)")
	}

	models := m.prepareExecutionModels(auth, "gpt-5.5")
	if len(models) != 1 || models[0] != "gpt-5.4" {
		t.Fatalf("prepareExecutionModels(configured alias target) = %v, want [%q]", models, "gpt-5.4")
	}
}

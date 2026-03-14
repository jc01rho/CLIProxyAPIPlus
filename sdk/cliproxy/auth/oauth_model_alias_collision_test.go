package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
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

	resolved := m.resolveOAuthUpstreamModel(auth, "gpt-5.4")
	if resolved != "gpt-5.4" {
		t.Fatalf("resolveOAuthUpstreamModel(collision real-first) = %q, want %q", resolved, "gpt-5.4")
	}

	resolvedWithSuffix := m.resolveOAuthUpstreamModel(auth, "gpt-5.4(high)")
	if resolvedWithSuffix != "gpt-5.4(high)" {
		t.Fatalf("resolveOAuthUpstreamModel(collision real-first with suffix) = %q, want %q", resolvedWithSuffix, "gpt-5.4(high)")
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
	if len(models) != 1 || models[0] != "gpt-5.4" {
		t.Fatalf("prepareExecutionModels(collision real-first) = %v, want [%q]", models, "gpt-5.4")
	}
}

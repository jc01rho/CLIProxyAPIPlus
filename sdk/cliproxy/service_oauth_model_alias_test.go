package cliproxy

import (
	"testing"

			internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestApplyOAuthModelAlias_Rename(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "g5" {
		t.Fatalf("expected model id %q, got %q", "g5", out[0].ID)
	}
	if out[0].Name != "models/g5" {
		t.Fatalf("expected model name %q, got %q", "models/g5", out[0].Name)
	}
}

func TestApplyOAuthModelAlias_ForkAddsAlias(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
	if out[1].ExecutionTarget != "gpt-5" {
		t.Fatalf("expected forked execution target %q, got %q", "gpt-5", out[1].ExecutionTarget)
	}
}

func TestApplyOAuthModelAlias_ForkAddsMultipleAliases(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
				{Name: "gpt-5", Alias: "g5-2", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 3 {
		t.Fatalf("expected 3 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
	if out[2].ID != "g5-2" {
		t.Fatalf("expected third model id %q, got %q", "g5-2", out[2].ID)
	}
	if out[2].Name != "models/g5-2" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5-2", out[2].Name)
	}
	if out[1].ExecutionTarget != "gpt-5" {
		t.Fatalf("expected second execution target %q, got %q", "gpt-5", out[1].ExecutionTarget)
	}
	if out[2].ExecutionTarget != "gpt-5" {
		t.Fatalf("expected third execution target %q, got %q", "gpt-5", out[2].ExecutionTarget)
	}
}

func TestApplyOAuthModelAlias_DefaultGitHubCopilotAliasViaSanitize(t *testing.T) {
	cfg := &config.Config{}
	cfg.SanitizeOAuthModelAlias()

	models := []*ModelInfo{
		{ID: "claude-opus-4.6", Name: "models/claude-opus-4.6"},
	}

	out := applyOAuthModelAlias(cfg, "github-copilot", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models (original + default alias), got %d", len(out))
	}
	if out[0].ID != "claude-opus-4.6" {
		t.Fatalf("expected first model id %q, got %q", "claude-opus-4.6", out[0].ID)
	}
	if out[1].ID != "claude-opus-4-6" {
		t.Fatalf("expected second model id %q, got %q", "claude-opus-4-6", out[1].ID)
	}
	if out[1].Name != "models/claude-opus-4-6" {
		t.Fatalf("expected aliased model name %q, got %q", "models/claude-opus-4-6", out[1].Name)
	}
	if out[1].ExecutionTarget != "claude-opus-4.6" {
		t.Fatalf("expected aliased execution target %q, got %q", "claude-opus-4.6", out[1].ExecutionTarget)
	}
}

func TestApplyOAuthModelAlias_RealModelWinsOnAliasCollision(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"github-copilot": {
				{Name: "gpt-5.2-codex", Alias: "gpt-5.4", Fork: true},

func TestApplyOAuthModelAlias_PluginProvider(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"qoder": {
				{Name: "qmodel_latest", Alias: "qlatest"},

			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5.2-codex", Name: "models/gpt-5.2-codex"},
		{ID: "gpt-5.4", Name: "models/gpt-5.4"},
	}

	out := applyOAuthModelAlias(cfg, "github-copilot", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5.2-codex" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5.2-codex", out[0].ID)
	}
	if out[1].ID != "gpt-5.4" {
		t.Fatalf("expected second model id %q, got %q", "gpt-5.4", out[1].ID)
	}
	if out[1].Name != "models/gpt-5.4" {
		t.Fatalf("expected real model name %q, got %q", "models/gpt-5.4", out[1].Name)
	}
	if out[1].ExecutionTarget != "" {
		t.Fatalf("expected real model execution target to stay empty, got %q", out[1].ExecutionTarget)
	}
}

func TestApplyOAuthModelAlias_RealModelWinsOnCodexAliasCollision(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5.4", Alias: "gpt-5.5", Fork: true},

		{ID: "qmodel_latest", Name: "models/qmodel_latest"},
	}

	out := applyOAuthModelAlias(cfg, "qoder", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "qlatest" {
		t.Fatalf("expected plugin alias id %q, got %q", "qlatest", out[0].ID)
	}
	if out[0].Name != "models/qlatest" {
		t.Fatalf("expected plugin alias name %q, got %q", "models/qlatest", out[0].Name)
	}
}

func TestApplyOAuthModelAlias_PluginProviderSkipsAPIKey(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"qoder": {
				{Name: "qmodel_latest", Alias: "qlatest"},

			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5.4", Name: "models/gpt-5.4"},
		{ID: "gpt-5.5", Name: "models/gpt-5.5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5.4" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5.4", out[0].ID)
	}
	if out[1].ID != "gpt-5.5" {
		t.Fatalf("expected second model id %q, got %q", "gpt-5.5", out[1].ID)
	}
	if out[1].Name != "models/gpt-5.5" {
		t.Fatalf("expected real model name %q, got %q", "models/gpt-5.5", out[1].Name)
	}
	if out[1].ExecutionTarget != "" {
		t.Fatalf("expected real model execution target to stay empty, got %q", out[1].ExecutionTarget)
	}
}

func TestRegisterModelsForAuth_ClaudeOAuthAliasSetsExecutionTarget(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"claude": {
					{Name: "claude-sonnet-4-6", Alias: "sonnet", Fork: true},
				},
			},
			ClaudeKey: []config.ClaudeKey{{
				APIKey: "key-123",
				Models: []internalconfig.ClaudeModel{{
					Name:  "claude-sonnet-4-6",
					Alias: "claude-sonnet-4-6",
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-claude-oauth-alias-registration",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := reg.GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("expected registered models for claude oauth auth")
	}

	var original, alias *ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch model.ID {
		case "claude-sonnet-4-6":
			original = model
		case "sonnet":
			alias = model
		}
	}
	if original == nil {
		t.Fatal("expected original claude-sonnet-4-6 model to be registered")
	}
	if alias == nil {
		t.Fatal("expected aliased sonnet model to be registered")
	}
	if alias.ExecutionTarget != "claude-sonnet-4-6" {
		t.Fatalf("alias execution target = %q, want %q", alias.ExecutionTarget, "claude-sonnet-4-6")
	}
	if original.ExecutionTarget != "" {
		t.Fatalf("original model execution target = %q, want empty", original.ExecutionTarget)
	}
}

		{ID: "qmodel_latest", Name: "models/qmodel_latest"},
	}

	out := applyOAuthModelAlias(cfg, "qoder", "api_key", models)
	if len(out) != 1 || out[0].ID != "qmodel_latest" {
		t.Fatalf("expected API key plugin model to remain unchanged, got %#v", out)
	}
}


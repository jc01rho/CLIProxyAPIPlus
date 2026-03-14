package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestResolveOAuthUpstreamModel_SuffixPreservation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		aliases map[string][]internalconfig.OAuthModelAlias
		channel string
		input   string
		want    string
	}{
		{
			name: "numeric suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"}},
			},
			channel: "gemini-cli",
			input:   "gemini-2.5-pro(8192)",
			want:    "gemini-2.5-pro-exp-03-25(8192)",
		},
		{
			name: "level suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"claude": {{Name: "claude-sonnet-4-5-20250514", Alias: "claude-sonnet-4-5"}},
			},
			channel: "claude",
			input:   "claude-sonnet-4-5(high)",
			want:    "claude-sonnet-4-5-20250514(high)",
		},
		{
			name: "no suffix unchanged",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"}},
			},
			channel: "gemini-cli",
			input:   "gemini-2.5-pro",
			want:    "gemini-2.5-pro-exp-03-25",
		},
		{
			name: "kiro alias resolves",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"kiro": {{Name: "kiro-claude-sonnet-4-5", Alias: "sonnet"}},
			},
			channel: "kiro",
			input:   "sonnet",
			want:    "kiro-claude-sonnet-4-5",
		},
		{
			name: "config suffix takes priority",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"claude": {{Name: "claude-sonnet-4-5-20250514(low)", Alias: "claude-sonnet-4-5"}},
			},
			channel: "claude",
			input:   "claude-sonnet-4-5(high)",
			want:    "claude-sonnet-4-5-20250514(low)",
		},
		{
			name: "auto suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"}},
			},
			channel: "gemini-cli",
			input:   "gemini-2.5-pro(auto)",
			want:    "gemini-2.5-pro-exp-03-25(auto)",
		},
		{
			name: "none suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"}},
			},
			channel: "gemini-cli",
			input:   "gemini-2.5-pro(none)",
			want:    "gemini-2.5-pro-exp-03-25(none)",
		},
		{
			name: "github-copilot suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"github-copilot": {{Name: "claude-opus-4.6", Alias: "opus"}},
			},
			channel: "github-copilot",
			input:   "opus(medium)",
			want:    "claude-opus-4.6(medium)",
		},
		{
			name: "github-copilot no suffix",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"github-copilot": {{Name: "claude-opus-4.6", Alias: "opus"}},
			},
			channel: "github-copilot",
			input:   "opus",
			want:    "claude-opus-4.6",
		},
		{
			name: "kimi suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"kimi": {{Name: "kimi-k2.5", Alias: "k2.5"}},
			},
			channel: "kimi",
			input:   "k2.5(high)",
			want:    "kimi-k2.5(high)",
		},
		{
			name: "case insensitive alias lookup with suffix",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "Gemini-2.5-Pro"}},
			},
			channel: "gemini-cli",
			input:   "gemini-2.5-pro(high)",
			want:    "gemini-2.5-pro-exp-03-25(high)",
		},
		{
			name: "no alias returns empty",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"}},
			},
			channel: "gemini-cli",
			input:   "unknown-model(high)",
			want:    "",
		},
		{
			name: "wrong channel returns empty",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"}},
			},
			channel: "claude",
			input:   "gemini-2.5-pro(high)",
			want:    "",
		},
		{
			name: "empty suffix filtered out",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"}},
			},
			channel: "gemini-cli",
			input:   "gemini-2.5-pro()",
			want:    "gemini-2.5-pro-exp-03-25",
		},
		{
			name: "incomplete suffix treated as no suffix",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro(high"}},
			},
			channel: "gemini-cli",
			input:   "gemini-2.5-pro(high",
			want:    "gemini-2.5-pro-exp-03-25",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr := NewManager(nil, nil, nil)
			mgr.SetConfig(&internalconfig.Config{})
			mgr.SetOAuthModelAlias(tt.aliases)

			auth := createAuthForChannel(tt.channel)
			got := mgr.resolveOAuthUpstreamModel(auth, tt.input)
			if got != tt.want {
				t.Errorf("resolveOAuthUpstreamModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func createAuthForChannel(channel string) *Auth {
	switch channel {
	case "gemini-cli":
		return &Auth{Provider: "gemini-cli"}
	case "claude":
		return &Auth{Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}
	case "vertex":
		return &Auth{Provider: "vertex", Attributes: map[string]string{"auth_kind": "oauth"}}
	case "codex":
		return &Auth{Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	case "aistudio":
		return &Auth{Provider: "aistudio"}
	case "antigravity":
		return &Auth{Provider: "antigravity"}
	case "qwen":
		return &Auth{Provider: "qwen"}
	case "iflow":
		return &Auth{Provider: "iflow"}
	case "kimi":
		return &Auth{Provider: "kimi"}
	case "kiro":
		return &Auth{Provider: "kiro"}
	case "github-copilot":
		return &Auth{Provider: "github-copilot"}
	default:
		return &Auth{Provider: channel}
	}
}

func TestOAuthModelAliasChannel_Kimi(t *testing.T) {
	t.Parallel()

	if got := OAuthModelAliasChannel("kimi", "oauth"); got != "kimi" {
		t.Fatalf("OAuthModelAliasChannel() = %q, want %q", got, "kimi")
	}
}

func TestOAuthModelAliasChannel_GitHubCopilot(t *testing.T) {
	t.Parallel()

	if got := OAuthModelAliasChannel("github-copilot", ""); got != "github-copilot" {
		t.Fatalf("OAuthModelAliasChannel() = %q, want %q", got, "github-copilot")
	}
}

func TestOAuthModelAliasChannel_Kiro(t *testing.T) {
	t.Parallel()

	if got := OAuthModelAliasChannel("kiro", ""); got != "kiro" {
		t.Fatalf("OAuthModelAliasChannel() = %q, want %q", got, "kiro")
	}
}

func TestApplyOAuthModelAlias_SuffixPreservation(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"gemini-cli": {{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "gemini-cli"}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "gemini-2.5-pro(8192)")
	if resolvedModel != "gemini-2.5-pro-exp-03-25(8192)" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "gemini-2.5-pro-exp-03-25(8192)")
	}
}

func TestResolveModelAliasPoolFromConfigModels_NamePrecedence(t *testing.T) {
	t.Parallel()

	// Test case: When requested model matches a direct name, it should be returned
	// WITHOUT attempting alias resolution. This tests the precedence fix where
	// name-matching happens before alias-matching.
	tests := []struct {
		name          string
		candidate     string
		models        []modelAliasEntry
		expectedFirst string
		expectEmpty   bool
	}{
		{
			name:      "direct name match skips alias",
			candidate: "gemini-2.5-pro",
			models: []modelAliasEntry{
				&mockModelAlias{name: "gemini-2.5-pro-exp-03-25", alias: "gemini-2.5-pro"},
				&mockModelAlias{name: "gemini-2.5-pro", alias: "g25p"},
			},
			// With name-first precedence, should return "gemini-2.5-pro" (direct match)
			expectedFirst: "gemini-2.5-pro",
		},
		{
			name:      "alias resolution when no name matches",
			candidate: "g25p",
			models: []modelAliasEntry{
				&mockModelAlias{name: "gemini-2.5-pro-exp-03-25", alias: "g25p"},
			},
			// When name doesn't match, alias should resolve
			expectedFirst: "gemini-2.5-pro-exp-03-25",
		},
		{
			name:      "no match returns empty",
			candidate: "unknown-model",
			models: []modelAliasEntry{
				&mockModelAlias{name: "gemini-2.5-pro-exp-03-25", alias: "g25p"},
			},
			expectEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := resolveModelAliasPoolFromConfigModels(tt.candidate, tt.models)
			if tt.expectEmpty {
				if len(pool) != 0 {
					t.Errorf("expected empty pool, got %v", pool)
				}
				return
			}
			if len(pool) == 0 {
				t.Errorf("expected non-empty pool, got empty")
				return
			}
			if pool[0] != tt.expectedFirst {
				t.Errorf("expected first element %q, got %q", tt.expectedFirst, pool[0])
			}
		})
	}
}

type mockModelAlias struct {
	name  string
	alias string
}

func (m *mockModelAlias) GetName() string {
	return m.name
}

func (m *mockModelAlias) GetAlias() string {
	return m.alias
}

// TestResolveOAuthUpstreamModel_OriginalTakesPlacePriority tests that when a requested model matches
// an original/upstream model name, it is returned immediately without alias resolution.
func TestResolveOAuthUpstreamModel_OriginalTakesPlacePriority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		aliases map[string][]internalconfig.OAuthModelAlias
		channel string
		input   string
		want    string
	}{
		{
			name: "original model name matches - returns as-is",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {
					{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"},
				},
			},
			channel: "gemini-cli",
			input:   "gemini-2.5-pro-exp-03-25", // Request original name
			want:    "gemini-2.5-pro-exp-03-25", // Should return it immediately
		},
		{
			name: "original model name matches with suffix - preserves suffix",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"claude": {
					{Name: "claude-sonnet-4-5-20250514", Alias: "claude-sonnet-4-5"},
				},
			},
			channel: "claude",
			input:   "claude-sonnet-4-5-20250514(high)", // Original with suffix
			want:    "claude-sonnet-4-5-20250514(high)", // Should return original+suffix
		},
		{
			name: "original model name matches exactly - ignores alias",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {
					{Name: "codex-upstream-001", Alias: "codex-alias"},
					{Name: "codex-alternative-002", Alias: "codex-upstream-001"},
				},
			},
			channel: "codex",
			input:   "codex-upstream-001", // Request original name (which is also an alias!)
			want:    "codex-upstream-001", // Should return original, NOT resolve alias
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.SetOAuthModelAlias(tc.aliases)

			auth := createAuthForChannel(tc.channel)

			result := m.resolveOAuthUpstreamModel(auth, tc.input)

			if result != tc.want {
				t.Errorf("resolveOAuthUpstreamModel() = %q, want %q", result, tc.want)
			}
		})
	}
}

// TestResolveOAuthUpstreamModel_FallbackToAlias tests that when a requested model
// doesn't match an original, it falls back to alias resolution.
func TestResolveOAuthUpstreamModel_FallbackToAlias(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		aliases map[string][]internalconfig.OAuthModelAlias
		channel string
		input   string
		want    string
	}{
		{
			name: "alias resolves when original not requested",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"gemini-cli": {
					{Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"},
				},
			},
			channel: "gemini-cli",
			input:   "gemini-2.5-pro",           // Request alias
			want:    "gemini-2.5-pro-exp-03-25", // Should resolve to original
		},
		{
			name: "alias with suffix resolves and preserves suffix",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"claude": {
					{Name: "claude-opus-4-5-20251101", Alias: "claude-opus-4-5"},
				},
			},
			channel: "claude",
			input:   "claude-opus-4-5(high)",          // Alias with suffix
			want:    "claude-opus-4-5-20251101(high)", // Should resolve + preserve suffix
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.SetOAuthModelAlias(tc.aliases)

			auth := createAuthForChannel(tc.channel)

			result := m.resolveOAuthUpstreamModel(auth, tc.input)

			if result != tc.want {
				t.Errorf("resolveOAuthUpstreamModel() = %q, want %q", result, tc.want)
			}
		})
	}
}

// TestResolveModelAliasPoolFromConfigModels_OriginalFirst tests that when resolving
// model alias pools, original model names take priority over aliases.
func TestResolveModelAliasPoolFromConfigModels_OriginalFirst(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		candidate     string
		models        []modelAliasEntry
		expectedFirst string
		expectEmpty   bool
	}{
		{
			name:      "original model name requested - returns original",
			candidate: "gemini-2.5-pro",
			models: []modelAliasEntry{
				&mockModelAlias{name: "gemini-2.5-pro", alias: "g25p"},
				&mockModelAlias{name: "gemini-2.5-pro-exp-03-25", alias: "gemini-2.5-pro-exp"},
			},
			expectedFirst: "gemini-2.5-pro", // Original name match takes priority
		},
		{
			name:      "alias resolves when original not requested",
			candidate: "g25p",
			models: []modelAliasEntry{
				&mockModelAlias{name: "gemini-2.5-pro", alias: "g25p"},
			},
			expectedFirst: "gemini-2.5-pro", // Alias resolves to original
		},
		{
			name:      "multiple aliases - first match returns",
			candidate: "alias-1",
			models: []modelAliasEntry{
				&mockModelAlias{name: "model-a", alias: "alias-1"},
				&mockModelAlias{name: "model-b", alias: "alias-1"},
			},
			expectedFirst: "model-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := resolveModelAliasPoolFromConfigModels(tt.candidate, tt.models)
			if tt.expectEmpty {
				if len(pool) != 0 {
					t.Errorf("expected empty pool, got %v", pool)
				}
				return
			}
			if len(pool) == 0 {
				t.Errorf("expected non-empty pool, got empty")
				return
			}
			if pool[0] != tt.expectedFirst {
				t.Errorf("expected first element %q, got %q", tt.expectedFirst, pool[0])
			}
		})
	}
}

func TestResolveOAuthUpstreamModel_RegisteredRealModelTakesPriority(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"github-copilot": {
			{Name: "gpt-5.2-codex", Alias: "gpt-5.4"},
		},
	})

	auth := &Auth{
		ID:       "oauth-real-model-priority",
		Provider: "github-copilot",
		Metadata: map[string]any{"username": "tester"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "github-copilot", []*registry.ModelInfo{
		{ID: "gpt-5.4"},
		{ID: "gpt-5.2-codex"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	resolved := m.resolveOAuthUpstreamModel(auth, "gpt-5.4")
	if resolved != "gpt-5.4" {
		t.Fatalf("resolveOAuthUpstreamModel(real model) = %q, want %q", resolved, "gpt-5.4")
	}

	aliased := m.resolveOAuthUpstreamModel(auth, "gpt-5.4(high)")
	if aliased != "gpt-5.4(high)" {
		t.Fatalf("resolveOAuthUpstreamModel(real model with suffix) = %q, want %q", aliased, "gpt-5.4(high)")
	}
}

func TestResolveOAuthUpstreamModel_AliasExposedModelUsesExecutionTarget(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.2", Alias: "gpt-5.4", Fork: true},
		},
	})

	auth := &Auth{
		ID:       "oauth-alias-exposed-model",
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
		{ID: "gpt-5.2"},
		{ID: "gpt-5.4", ExecutionTarget: "gpt-5.2"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	resolved := m.resolveOAuthUpstreamModel(auth, "gpt-5.4")
	if resolved != "gpt-5.2" {
		t.Fatalf("resolveOAuthUpstreamModel(alias-exposed model) = %q, want %q", resolved, "gpt-5.2")
	}

	resolvedWithSuffix := m.resolveOAuthUpstreamModel(auth, "gpt-5.4(high)")
	if resolvedWithSuffix != "gpt-5.2(high)" {
		t.Fatalf("resolveOAuthUpstreamModel(alias-exposed model with suffix) = %q, want %q", resolvedWithSuffix, "gpt-5.2(high)")
	}
}

func TestPrepareExecutionModels_AuthSpecificRealFirstAliasSecond(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.2", Alias: "gpt-5.4", Fork: true},
		},
	})

	authA := &Auth{
		ID:       "codex-auth-a",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"username": "a"},
	}
	authB := &Auth{
		ID:       "codex-auth-b",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"username": "b"},
	}
	if _, err := m.Register(context.Background(), authA); err != nil {
		t.Fatalf("register authA: %v", err)
	}
	if _, err := m.Register(context.Background(), authB); err != nil {
		t.Fatalf("register authB: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authA.ID, "codex", []*registry.ModelInfo{
		{ID: "gpt-5.4"},
		{ID: "gpt-5.2"},
	})
	reg.RegisterClient(authB.ID, "codex", []*registry.ModelInfo{
		{ID: "gpt-5.2"},
		{ID: "gpt-5.4", ExecutionTarget: "gpt-5.2"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authA.ID)
		reg.UnregisterClient(authB.ID)
	})

	modelsA := m.prepareExecutionModels(authA, "gpt-5.4")
	if len(modelsA) != 1 || modelsA[0] != "gpt-5.4" {
		t.Fatalf("prepareExecutionModels(authA) = %v, want [%q]", modelsA, "gpt-5.4")
	}

	modelsB := m.prepareExecutionModels(authB, "gpt-5.4")
	if len(modelsB) != 1 || modelsB[0] != "gpt-5.2" {
		t.Fatalf("prepareExecutionModels(authB) = %v, want [%q]", modelsB, "gpt-5.2")
	}
}

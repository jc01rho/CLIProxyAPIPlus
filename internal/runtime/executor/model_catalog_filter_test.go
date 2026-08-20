package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestFilterKiloModelsKeepsOnlyFree(t *testing.T) {
	got := FilterKiloModels([]*registry.ModelInfo{
		{ID: "kilo/auto", DisplayName: "Kilo Auto"},
		{ID: "openrouter/free", DisplayName: "Free Models Router"},
		{ID: "openai/gpt-5.5", DisplayName: "GPT-5.5"},
		{ID: "minimax/minimax-m2.5:free", DisplayName: "MiniMax M2.5"},
		{ID: "anthropic/claude-sonnet-4.6", DisplayName: "Claude Sonnet 4.6"},
	})
	assertModelIDs(t, got, "openrouter/free", "minimax/minimax-m2.5:free")
}

func TestFilterKiloModelsDropsPaidStaticCatalog(t *testing.T) {
	got := FilterKiloModels(registry.GetKiloModels())
	if len(got) == 0 {
		t.Fatal("expected at least openrouter/free")
	}
	for _, model := range got {
		if !catalogContains(model, "free") {
			t.Fatalf("kept non-free kilo model %q (%q)", model.ID, model.DisplayName)
		}
	}
}

func TestFilterCursorModelsKeepsFreeComposerGrokOpusSonnetKimi(t *testing.T) {
	got := FilterCursorModels([]*registry.ModelInfo{
		{ID: "composer-2.5", DisplayName: "Composer 2.5"},
		{ID: "claude-4-sonnet", DisplayName: "Claude 4 Sonnet"},
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
		{ID: "grok-4.5", DisplayName: "Grok 4.5"},
		{ID: "cursor-small", DisplayName: "Cursor Small"},
		{ID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash"},
		{ID: "some-free-tier", DisplayName: "Some Free Tier"},
		{ID: "claude-opus-5", DisplayName: "Claude Opus 5"},
		{ID: "kimi-k3", DisplayName: "Kimi K3"},
	})
	assertModelIDs(t, got, "composer-2.5", "claude-4-sonnet", "grok-4.5", "some-free-tier", "claude-opus-5", "kimi-k3")
}

func TestFetchCursorModelsWithoutTokenKeepsAllowedFamiliesOnly(t *testing.T) {
	got := FetchCursorModels(context.Background(), nil, nil)
	if len(got) == 0 {
		t.Fatal("expected filtered cursor fallback models")
	}
	for _, model := range got {
		if !catalogContains(model, "free", "composer", "grok", "opus", "sonnet", "kimi") {
			t.Fatalf("kept disallowed cursor model %q (%q)", model.ID, model.DisplayName)
		}
	}
	var sawOpus, sawSonnet, sawKimi bool
	for _, model := range got {
		if catalogContains(model, "opus") {
			sawOpus = true
		}
		if catalogContains(model, "sonnet") {
			sawSonnet = true
		}
		if catalogContains(model, "kimi-k3") {
			sawKimi = true
		}
	}
	if !sawOpus {
		t.Fatal("expected an opus model in cursor fallback catalog")
	}
	if !sawSonnet {
		t.Fatal("expected a sonnet model in cursor fallback catalog")
	}
	if !sawKimi {
		t.Fatal("expected kimi-k3 in cursor fallback catalog")
	}
}

func TestFilterKiroModelsKeepsOnlyClaude(t *testing.T) {
	got := FilterKiroModels([]*registry.ModelInfo{
		{ID: "kiro-auto", DisplayName: "Kiro Auto"},
		{ID: "kiro-claude-sonnet-5", DisplayName: "Kiro Claude Sonnet 5"},
		{ID: "kiro-gpt-5-6-sol", DisplayName: "Kiro GPT-5.6 Sol"},
		{ID: "kiro-minimax-m2-5", DisplayName: "Kiro MiniMax M2.5"},
		{ID: "kiro-claude-opus-4-7", DisplayName: "Kiro Claude Opus 4.7"},
		{ID: "kiro-glm-5", DisplayName: "Kiro GLM-5"},
	})
	assertModelIDs(t, got, "kiro-claude-sonnet-5", "kiro-claude-opus-4-7")
}

func TestFilterKiroModelsDropsNonClaudeStaticCatalog(t *testing.T) {
	got := FilterKiroModels(registry.GetKiroModels())
	if len(got) == 0 {
		t.Fatal("expected at least one claude model")
	}
	for _, model := range got {
		if !catalogContains(model, "claude") {
			t.Fatalf("kept non-claude kiro model %q (%q)", model.ID, model.DisplayName)
		}
	}
}

func assertModelIDs(t *testing.T, models []*registry.ModelInfo, want ...string) {
	t.Helper()
	got := make([]string, 0, len(models))
	for _, model := range models {
		got = append(got, model.ID)
	}
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}

func catalogContains(model *registry.ModelInfo, needles ...string) bool {
	id := strings.ToLower(model.ID)
	name := strings.ToLower(model.DisplayName)
	for _, needle := range needles {
		if strings.Contains(id, needle) || strings.Contains(name, needle) {
			return true
		}
	}
	return false
}

package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestNormalizeCompatConfigEndpoints_ExplicitWins(t *testing.T) {
	got := normalizeCompatConfigEndpoints(
		[]string{"/responses", "chat/completions", "/responses", ""},
		"https://api.example.com/v1", "any-model", "any-alias",
	)
	if len(got) != 2 || got[0] != "/responses" || got[1] != "/chat/completions" {
		t.Fatalf("explicit endpoints normalized wrong: %v", got)
	}
}

func TestNormalizeCompatConfigEndpoints_OpencodeZenMuseAutoResponses(t *testing.T) {
	// OmniRoute PR 12675: muse-spark serves only /v1/responses; the
	// executor's chat/completions fallback 500s upstream.
	for _, name := range []string{"muse-spark-1.3-contributor-free", "Muse-Spark-1.2", "my-muse-free-alias"} {
		got := normalizeCompatConfigEndpoints(nil, "https://opencode.ai/zen/v1", name, name)
		if len(got) != 1 || got[0] != "/responses" {
			t.Fatalf("zen muse %q auto-endpoint = %v, want [/responses]", name, got)
		}
	}
}

func TestNormalizeCompatConfigEndpoints_OpencodeNonMuseStaysEmpty(t *testing.T) {
	// Non-muse Zen models accept both wire shapes; no auto-declaration.
	got := normalizeCompatConfigEndpoints(nil, "https://opencode.ai/zen/v1", "nemotron-3-ultra-free", "nemotron-3-ultra-free")
	if len(got) != 0 {
		t.Fatalf("zen nemotron auto-endpoint = %v, want empty", got)
	}
}

func TestNormalizeCompatConfigEndpoints_NonOpencodeUntouched(t *testing.T) {
	got := normalizeCompatConfigEndpoints(nil, "https://api.openrouter.ai/v1", "muse-spark-1.3-contributor-free", "muse-spark")
	if len(got) != 0 {
		t.Fatalf("non-opencode muse auto-endpoint = %v, want empty (branding gate)", got)
	}
}

func TestBuildOpenAICompatibilityConfigModels_CarriesEndpoints(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Name:    "opencode-free",
		BaseURL: "https://opencode.ai/zen/v1",
		Models: []config.OpenAICompatibilityModel{
			{Name: "muse-spark-1.3-contributor-free", Alias: "muse-spark-1.3-contributor-free"},
			{Name: "deepseek-v4-flash-free", Alias: "deepseek-v4-flash-free"},
		},
	}
	models := buildOpenAICompatibilityConfigModels(compat)
	if len(models) != 2 {
		t.Fatalf("models = %d, want 2", len(models))
	}
	if got := models[0].SupportedEndpoints; len(got) != 1 || got[0] != "/responses" {
		t.Fatalf("muse SupportedEndpoints = %v, want [/responses]", got)
	}
	if got := models[1].SupportedEndpoints; len(got) != 0 {
		t.Fatalf("deepseek SupportedEndpoints = %v, want empty", got)
	}
}

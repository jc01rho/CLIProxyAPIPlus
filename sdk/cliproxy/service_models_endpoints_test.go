package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestNormalizeCompatConfigEndpoints_ExplicitWins(t *testing.T) {
	got := normalizeCompatConfigEndpoints(
		[]string{"/responses", "chat/completions", "/responses", ""},
		"https://api.example.com/v1",
	)
	if len(got) != 2 || got[0] != "/responses" || got[1] != "/chat/completions" {
		t.Fatalf("explicit endpoints normalized wrong: %v", got)
	}
}

func TestNormalizeCompatConfigEndpoints_ZenGatewayAutoResponses(t *testing.T) {
	// Any model on the OpenCode Zen gateway declares /responses — the
	// declaration is a property of the gateway host, not the model name.
	for _, name := range []string{
		"muse-spark-1.3-contributor-free",
		"deepseek-v4-flash-free",
		"mimo-v2.5-free",
		"opencode",
		"claude-sonnet-4-6",
	} {
		got := normalizeCompatConfigEndpoints(nil, "https://opencode.ai/zen/v1")
		if len(got) != 1 || got[0] != "/responses" {
			t.Fatalf("zen model %q auto-endpoint = %v, want [/responses]", name, got)
		}
	}
	// Subdomain of opencode.ai counts too.
	if got := normalizeCompatConfigEndpoints(nil, "https://api.opencode.ai/v1"); len(got) != 1 {
		t.Fatalf("api.opencode.ai = %v, want [/responses]", got)
	}
}

func TestNormalizeCompatConfigEndpoints_NonZenUntouched(t *testing.T) {
	// muse-spark on a NON-Zen gateway stays dual-shape: the gate is the
	// opencode.ai host, never the model name.
	for _, base := range []string{
		"https://api.openrouter.ai/v1",
		"https://my-opencode-clone.example.com/v1",
		"https://example.com/opencode.ai",
	} {
		if got := normalizeCompatConfigEndpoints(nil, base); len(got) != 0 {
			t.Fatalf("non-zen base %q auto-endpoint = %v, want empty", base, got)
		}
	}
}

func TestOpencodeZenGatewayBaseURL(t *testing.T) {
	yes := []string{"https://opencode.ai/zen/v1", "https://opencode.ai", "https://api.opencode.ai/v1", "http://Opencode.AI/zen"}
	no := []string{"https://notopencode.ai/v1", "https://example.com/opencode.ai", "https://opencode.ai.evil.com/v1", "", "https://localhost:8317"}
	for _, u := range yes {
		if !opencodeZenGatewayBaseURL(u) {
			t.Errorf("opencodeZenGatewayBaseURL(%q) = false, want true", u)
		}
	}
	for _, u := range no {
		if opencodeZenGatewayBaseURL(u) {
			t.Errorf("opencodeZenGatewayBaseURL(%q) = true, want false", u)
		}
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
	for i, m := range models {
		if got := m.SupportedEndpoints; len(got) != 1 || got[0] != "/responses" {
			t.Fatalf("model %d SupportedEndpoints = %v, want [/responses] (gateway-wide)", i, got)
		}
	}
}

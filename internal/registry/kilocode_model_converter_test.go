package registry

import (
	"testing"
)

func TestResolveKilocodeModelAlias(t *testing.T) {
	tests := []struct {
		name     string
		alias    string
		expected string
	}{
		// Explicit aliases
		{"kimi short", "kimi", "moonshotai/kimi-k2.5:free"},
		{"kimi2 short", "kimi2", "moonshotai/kimi-k2.5:free"},
		{"kimi-k2 short", "kimi-k2", "moonshotai/kimi-k2.5:free"},
		{"kimi-k2.5 full", "kimi-k2.5", "moonshotai/kimi-k2.5:free"},
		{"glm short", "glm", "z-ai/glm-4.7:free"},
		{"glm4 short", "glm4", "z-ai/glm-4.7:free"},
		{"glm-4 short", "glm-4", "z-ai/glm-4.7:free"},
		{"glm-4.7 full", "glm-4.7", "z-ai/glm-4.7:free"},
		{"minimax short", "minimax", "minimax/minimax-m2.1:free"},
		{"trinity short", "trinity", "arcee-ai/trinity-large-preview:free"},
		{"corethink short", "corethink", "corethink:free"},

		// Case insensitivity
		{"KIMI uppercase", "KIMI", "moonshotai/kimi-k2.5:free"},
		{"Kimi2 mixed case", "Kimi2", "moonshotai/kimi-k2.5:free"},
		{"GLM uppercase", "GLM", "z-ai/glm-4.7:free"},

		// kilocode- prefix stripping
		{"with kilocode prefix", "kilocode-kimi2", "moonshotai/kimi-k2.5:free"},

		// Already full format
		{"already full format", "moonshotai/kimi-k2.5:free", "moonshotai/kimi-k2.5:free"},

		// Unknown model passthrough
		{"unknown model", "unknown-model", "unknown-model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveKilocodeModelAlias(tt.alias)
			if result != tt.expected {
				t.Errorf("ResolveKilocodeModelAlias(%q) = %q, want %q", tt.alias, result, tt.expected)
			}
		})
	}
}

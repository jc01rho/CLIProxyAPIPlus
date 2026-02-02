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
		// kilocode- prefix stripping
		{"with kilocode prefix full format", "kilocode-moonshotai/kimi-k2.5:free", "moonshotai/kimi-k2.5:free"},
		{"with kilocode prefix simple", "kilocode-kimi", "kimi"},

		// Already full format (passthrough)
		{"already full format", "moonshotai/kimi-k2.5:free", "moonshotai/kimi-k2.5:free"},
		{"already full format glm", "z-ai/glm-4.7:free", "z-ai/glm-4.7:free"},

		// Short names passthrough (config alias handles these)
		{"kimi short passthrough", "kimi", "kimi"},
		{"glm short passthrough", "glm", "glm"},
		{"unknown model passthrough", "unknown-model", "unknown-model"},

		// Edge cases
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},
		{"whitespace around", "  kimi  ", "kimi"},
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

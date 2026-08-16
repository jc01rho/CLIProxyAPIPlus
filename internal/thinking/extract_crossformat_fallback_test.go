package thinking

import "testing"

func TestExtractGeminiConfigCrossFormatPrefixFallback(t *testing.T) {
	cases := []struct {
		name string
		body string
		prov string
		want ThinkingConfig
	}{
		{
			name: "gemini target reads native prefix",
			body: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096}}}`,
			prov: "gemini",
			want: ThinkingConfig{Mode: ModeBudget, Budget: 4096},
		},
		{
			name: "gemini-cli target reads its native prefix",
			body: `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":2048}}}}`,
			prov: "gemini-cli",
			want: ThinkingConfig{Mode: ModeBudget, Budget: 2048},
		},
		{
			name: "gemini target falls back to gemini-cli prefix",
			body: `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}}`,
			prov: "gemini",
			want: ThinkingConfig{Mode: ModeBudget, Budget: 64000},
		},
		{
			name: "gemini-cli target falls back to gemini prefix",
			body: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":48000}}}`,
			prov: "gemini-cli",
			want: ThinkingConfig{Mode: ModeBudget, Budget: 48000},
		},
		{
			name: "thinkingLevel survives cross-format fallback",
			body: `{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}}`,
			prov: "gemini",
			want: ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel("high")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractGeminiConfig([]byte(tc.body), tc.prov)
			if got != tc.want {
				t.Fatalf("got %+v, want %+v (body=%s, prov=%s)", got, tc.want, tc.body, tc.prov)
			}
		})
	}
}

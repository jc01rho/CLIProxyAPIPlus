package util

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestPreferOpenAICompatOverClaude(t *testing.T) {
	reg := registry.GetGlobalRegistry()

	reg.RegisterClient("test-codex", "codex", []*registry.ModelInfo{
		{ID: "gpt-4o", Type: "openai-compatibility"},
	})
	reg.RegisterClient("test-cursor", "cursor", []*registry.ModelInfo{
		{ID: "gpt-4o", Type: "cursor"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient("test-codex")
		reg.UnregisterClient("test-cursor")
	})

	tests := []struct {
		name      string
		modelID   string
		providers []string
		want      []string
	}{
		{
			name:      "no claude in providers",
			modelID:   "gpt-4o",
			providers: []string{"codex", "cursor"},
			want:      []string{"codex", "cursor"},
		},
		{
			name:      "claude only provider",
			modelID:   "gpt-4o",
			providers: []string{"claude"},
			want:      []string{"claude"},
		},
		{
			name:      "claude + openai-compat provider removes claude",
			modelID:   "gpt-4o",
			providers: []string{"claude", "codex"},
			want:      []string{"codex"},
		},
		{
			name:      "claude + non-compat provider keeps both",
			modelID:   "gpt-4o",
			providers: []string{"claude", "cursor"},
			want:      []string{"claude", "cursor"},
		},
		{
			name:      "claude + compat + non-compat removes only claude",
			modelID:   "gpt-4o",
			providers: []string{"claude", "codex", "cursor"},
			want:      []string{"codex", "cursor"},
		},
		{
			name:      "empty providers",
			modelID:   "gpt-4o",
			providers: []string{},
			want:      []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := preferOpenAICompatOverClaude(tc.modelID, tc.providers)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want=%d, got=%v want=%v", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("index %d: got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

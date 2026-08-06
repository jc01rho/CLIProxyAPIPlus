package registry

import "testing"

// TestClineModelsMatch9Router asserts the static Cline catalog matches the
// 9Router (decolua/9router master) model list as of 2026-07-30. Keeping the
// IDs in sync prevents routing drift when a model is added/removed upstream.
func TestClineModelsMatch9Router(t *testing.T) {
	wantIDs := []string{
		"anthropic/claude-opus-4.7",
		"anthropic/claude-sonnet-4.6",
		"anthropic/claude-opus-4.6",
		"openai/gpt-5.3-codex",
		"openai/gpt-5.4",
		"google/gemini-3.1-pro-preview",
		"google/gemini-3.1-flash-lite-preview",
		"kwaipilot/kat-coder-pro",
	}

	models := GetClineModels()
	if len(models) != len(wantIDs) {
		t.Fatalf("GetClineModels() returned %d models, want %d", len(models), len(wantIDs))
	}

	have := make(map[string]*ModelInfo, len(models))
	for _, m := range models {
		if m == nil {
			t.Fatal("GetClineModels() returned a nil model entry")
		}
		if m.Type != "cline" {
			t.Errorf("model %q Type = %q, want cline", m.ID, m.Type)
		}
		if m.OwnedBy != "cline" {
			t.Errorf("model %q OwnedBy = %q, want cline", m.ID, m.OwnedBy)
		}
		if m.ContextLength <= 0 {
			t.Errorf("model %q ContextLength = %d, want > 0", m.ID, m.ContextLength)
		}
		if m.MaxCompletionTokens <= 0 {
			t.Errorf("model %q MaxCompletionTokens = %d, want > 0", m.ID, m.MaxCompletionTokens)
		}
		have[m.ID] = m
	}

	for _, id := range wantIDs {
		if _, ok := have[id]; !ok {
			t.Errorf("GetClineModels() missing 9Router model %q", id)
		}
	}
}

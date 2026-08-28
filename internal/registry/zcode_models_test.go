package registry

import "testing"

// TestGetZcodeModels verifies the glm-zcode catalog exposes the glm-5.2 model
// with the Anthropic-compatible context window and max completion tokens.
func TestGetZcodeModels(t *testing.T) {
	models := GetZcodeModels()
	if len(models) == 0 {
		t.Fatal("expected at least one zcode model")
	}
	found := false
	for _, m := range models {
		if m.ID == "glm-5.2" {
			found = true
			if m.ContextLength != 1000000 {
				t.Errorf("glm-5.2 ContextLength = %d, want 1000000", m.ContextLength)
			}
			if m.MaxCompletionTokens != 131072 {
				t.Errorf("glm-5.2 MaxCompletionTokens = %d, want 131072", m.MaxCompletionTokens)
			}
			if m.Type != "claude" {
				t.Errorf("glm-5.2 Type = %q, want claude (Anthropic-compatible)", m.Type)
			}
		}
	}
	if !found {
		t.Fatal("glm-5.2 model not found in zcode catalog")
	}
}

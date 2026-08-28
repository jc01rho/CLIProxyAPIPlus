package claude

import (
	"encoding/json"
	"testing"
)

// TestClaudeUnknownRoleNormalizedAndAlternated verifies that unknown roles
// normalize to user and consecutive-user history gets synthetic assistants
// (kiro-lb normalize_message_roles + ensure_alternating_roles).
func TestClaudeUnknownRoleNormalizedAndAlternated(t *testing.T) {
	input := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1000,
		"messages": [
			{"role": "user", "content": "First"},
			{"role": "custom", "content": "Second"},
			{"role": "custom2", "content": "Third"},
			{"role": "user", "content": "Last"}
		]
	}`)

	result, _ := BuildKiroPayload(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	var roles []string
	for _, h := range payload.ConversationState.History {
		switch {
		case h.UserInputMessage != nil:
			roles = append(roles, "user")
		case h.AssistantResponseMessage != nil:
			roles = append(roles, "assistant")
		}
	}

	// custom roles normalize to user, so we get consecutive users that must
	// be separated by synthetic assistants.
	for i := 0; i < len(roles)-1; i++ {
		if roles[i] == "user" && roles[i+1] == "user" {
			t.Fatalf("Found consecutive user messages in history at %d: %v", i, roles)
		}
	}

	// All custom content preserved.
	var allContents []string
	for _, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil {
			allContents = append(allContents, h.UserInputMessage.Content)
		}
	}
	allContents = append(allContents, payload.ConversationState.CurrentMessage.UserInputMessage.Content)
	foundSecond := false
	for _, c := range allContents {
		if contains(c, "Second") || contains(c, "Third") {
			foundSecond = true
		}
	}
	if !foundSecond {
		t.Errorf("Expected normalized custom role content preserved, got: %v", allContents)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

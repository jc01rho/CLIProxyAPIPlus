package openai

import (
	"encoding/json"
	"testing"
)

// TestDeveloperRoleExtractedAsSystemPrompt verifies that OpenAI "developer"
// role messages are treated as system prompt (matching kiro-lb
// convert_openai_messages_to_unified which folds both system and developer
// into the system prompt).
func TestDeveloperRoleExtractedAsSystemPrompt(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-sonnet-4-5",
		"messages": [
			{"role": "system", "content": "System base"},
			{"role": "developer", "content": "Developer instruction"},
			{"role": "user", "content": "Hello"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	currentContent := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if currentContent == "" {
		t.Fatal("Expected current message to contain the system prompt")
	}
	if !contains(currentContent, "Developer instruction") {
		t.Errorf("Expected developer role content in system prompt, got: %q", currentContent)
	}
	if !contains(currentContent, "System base") {
		t.Errorf("Expected system role content in system prompt, got: %q", currentContent)
	}
}

// TestUnknownRoleNormalizedToUser verifies that unknown roles (e.g. "function")
// are normalized to user instead of being silently dropped, matching
// kiro-lb normalize_message_roles.
func TestUnknownRoleNormalizedToUser(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "custom", "content": "Custom role content"},
			{"role": "user", "content": "Second question"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// The custom role message must be preserved (normalized to user), not dropped.
	var allContents []string
	for _, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil {
			allContents = append(allContents, h.UserInputMessage.Content)
		}
	}
	allContents = append(allContents, payload.ConversationState.CurrentMessage.UserInputMessage.Content)

	foundCustom := false
	for _, c := range allContents {
		if contains(c, "Custom role content") {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Errorf("Expected custom role content to be preserved (normalized to user), got contents: %v", allContents)
	}
}

// TestAlternatingRolesInsertsSyntheticAssistant verifies that consecutive user
// messages in history get a synthetic assistant inserted between them, matching
// kiro-lb ensure_alternating_roles. Kiro rejects two consecutive
// userInputMessage entries. The trigger mirrors kiro-lb issue #64: unknown
// roles normalize to user, which can create consecutive user history entries.
func TestAlternatingRolesInsertsSyntheticAssistant(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "First"},
			{"role": "custom", "content": "Second"},
			{"role": "custom2", "content": "Third"},
			{"role": "custom3", "content": "Fourth"},
			{"role": "user", "content": "Last"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// History contains the first 4 (all user after normalize) plus synthetic
	// assistants. CurrentMessage is the last user "Last".
	var roles []string
	for _, h := range payload.ConversationState.History {
		switch {
		case h.UserInputMessage != nil:
			roles = append(roles, "user")
		case h.AssistantResponseMessage != nil:
			roles = append(roles, "assistant")
		}
	}

	if len(roles) < 4 {
		t.Fatalf("Expected at least 4 history entries (with synthetic assistants), got %d: %v", len(roles), roles)
	}

	// No two consecutive users in history.
	for i := 0; i < len(roles)-1; i++ {
		if roles[i] == "user" && roles[i+1] == "user" {
			t.Fatalf("Found consecutive user messages in history at %d: %v", i, roles)
		}
	}
}

// TestFirstMessageIsUserPrepend verifies that a conversation starting with an
// assistant message gets a placeholder user prepended so history alternation
// is correct (matching kiro-lb ensure_first_message_is_user and the existing
// claude converter behavior).
func TestFirstMessageIsUserPrepend(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-sonnet-4-5",
		"messages": [
			{"role": "assistant", "content": "Assistant opens"},
			{"role": "user", "content": "User follows"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	if len(payload.ConversationState.History) == 0 {
		t.Fatal("Expected history to be non-empty")
	}

	first := payload.ConversationState.History[0]
	if first.UserInputMessage == nil {
		t.Fatalf("Expected first history entry to be a user message, got: %+v", first)
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

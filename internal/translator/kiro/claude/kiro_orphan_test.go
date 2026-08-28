package claude

import (
	"encoding/json"
	"testing"
)

// TestClaudeOrphanedToolResultsPreservedAsText verifies that orphaned tool
// results (no matching tool_use in retained history) are preserved as text on
// the user message content rather than dropped, matching kiro-lb's
// orphaned-tool-result repair.
func TestClaudeOrphanedToolResultsPreservedAsText(t *testing.T) {
	input := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1000,
		"tools": [{"name": "Read", "input_schema": {"type": "object", "properties": {}}}],
		"messages": [
			{"role": "user", "content": "Read the file"},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "keep-1", "name": "Read", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "keep-1", "content": "file contents"},
					{"type": "tool_result", "tool_use_id": "orphan-1", "content": "orphaned result"}
				]
			},
			{"role": "user", "content": "What did it say?"}
		]
	}`)

	result, _ := BuildKiroPayload(input, "kiro-model", "", "CLI", false, false, nil, nil)

	payload := mustUnmarshalPayload(t, result)

	// Find the orphaned tool result text anywhere (history or current message).
	// tool_results message and the following user message merge into the
	// current message (both user role), so the orphan may land there.
	foundOrphanText := false
	for _, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil && contains(h.UserInputMessage.Content, "orphaned result") {
			foundOrphanText = true
		}
	}
	if contains(payload.ConversationState.CurrentMessage.UserInputMessage.Content, "orphaned result") {
		foundOrphanText = true
	}
	if !foundOrphanText {
		t.Fatal("Expected orphaned tool_result text preserved on user content")
	}
}

func mustUnmarshalPayload(t *testing.T, data []byte) KiroPayload {
	t.Helper()
	var payload KiroPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}
	return payload
}

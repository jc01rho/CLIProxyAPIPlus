package claude

import (
	"testing"
)

// TestClaudeStripToolContentWhenNoTools verifies that when no tools are
// defined but the conversation has tool_use/tool_result content blocks, they
// are converted to text (kiro-lb strip_all_tool_content). Kiro rejects
// toolResults referencing no tool definitions.
func TestClaudeStripToolContentWhenNoTools(t *testing.T) {
	input := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1000,
		"messages": [
			{"role": "user", "content": "Read the file"},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_strip", "name": "Read", "input": {"file": "/tmp/x"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_strip", "content": "File contents here"}
				]
			},
			{"role": "user", "content": "What did it say?"}
		]
	}`)

	// No tools array in the request.
	result, _ := BuildKiroPayload(input, "kiro-model", "", "CLI", false, false, nil, nil)

	payload := mustUnmarshalPayload(t, result)

	// Assert NO structured toolResults anywhere.
	var toolResultsFound bool
	for _, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil && h.UserInputMessage.UserInputMessageContext != nil {
			if len(h.UserInputMessage.UserInputMessageContext.ToolResults) > 0 {
				toolResultsFound = true
			}
		}
	}
	if ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext; ctx != nil {
		if len(ctx.ToolResults) > 0 {
			toolResultsFound = true
		}
	}
	if toolResultsFound {
		t.Fatal("Expected tool results stripped to text when no tools defined")
	}

	// The tool content must be preserved as text somewhere.
	var allContents []string
	for _, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil {
			allContents = append(allContents, h.UserInputMessage.Content)
		}
		if h.AssistantResponseMessage != nil {
			allContents = append(allContents, h.AssistantResponseMessage.Content)
		}
	}
	allContents = append(allContents, payload.ConversationState.CurrentMessage.UserInputMessage.Content)

	foundToolCall := false
	foundResult := false
	for _, c := range allContents {
		if contains(c, "Tool: Read") {
			foundToolCall = true
		}
		if contains(c, "File contents here") {
			foundResult = true
		}
	}
	if !foundToolCall {
		t.Errorf("Expected tool_use converted to text, got: %v", allContents)
	}
	if !foundResult {
		t.Errorf("Expected tool_result converted to text, got: %v", allContents)
	}
}

package openai

import (
	"encoding/json"
	"testing"
)

// TestStripAllToolContentWhenNoTools verifies that when a request has
// tool_calls + tool results but NO tools array defined, the tool content is
// converted to plain text (matching kiro-lb strip_all_tool_content). Kiro
// rejects toolResults that reference no tool definitions, so the conversation
// must be preserved as text instead of sending toolResults.
func TestStripAllToolContentWhenNoTools(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "Read the file"},
			{
				"role": "assistant",
				"content": "I'll read it.",
				"tool_calls": [
					{
						"id": "call_strip",
						"type": "function",
						"function": {
							"name": "Read",
							"arguments": "{\"file_path\": \"/tmp/x.txt\"}"
						}
					}
				]
			},
			{"role": "tool", "tool_call_id": "call_strip", "content": "File contents here"},
			{"role": "user", "content": "What did it say?"}
		]
	}`)

	// No tools in the request body.
	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Assert NO toolResults anywhere (history or current context).
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
		t.Fatal("Expected toolResults to be stripped to text when no tools are defined")
	}

	// The tool content must be preserved as text somewhere in the messages.
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

	foundRead := false
	foundResult := false
	for _, c := range allContents {
		if contains(c, "Tool: Read") || contains(c, "File contents here") {
			foundRead = true
		}
		if contains(c, "File contents here") {
			foundResult = true
		}
	}
	if !foundRead {
		t.Errorf("Expected tool call converted to text, got contents: %v", allContents)
	}
	if !foundResult {
		t.Errorf("Expected tool result converted to text, got contents: %v", allContents)
	}
}

// TestToolContentPreservedWhenToolsDefined verifies the existing behavior:
// when tools ARE defined, tool_calls/tool results stay as structured
// toolResults (not stripped to text).
func TestToolContentPreservedWhenToolsDefined(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-sonnet-4-5",
		"tools": [
			{"type": "function", "function": {"name": "Read", "parameters": {"type": "object", "properties": {}}}}
		],
		"messages": [
			{"role": "user", "content": "Read the file"},
			{
				"role": "assistant",
				"content": "I'll read it.",
				"tool_calls": [
					{"id": "call_keep", "type": "function", "function": {"name": "Read", "arguments": "{}"}}
				]
			},
			{"role": "tool", "tool_call_id": "call_keep", "content": "File contents here"},
			{"role": "user", "content": "What did it say?"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Tool results must remain structured in the current message context.
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil || len(ctx.ToolResults) != 1 || ctx.ToolResults[0].ToolUseID != "call_keep" {
		t.Fatalf("Expected tool result preserved when tools defined, got ctx: %+v", ctx)
	}
}

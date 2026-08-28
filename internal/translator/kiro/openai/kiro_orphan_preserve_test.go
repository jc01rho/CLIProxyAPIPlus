package openai

import (
	"testing"
)

// TestFilterOrphanedToolResults_PreservesText verifies that orphaned tool
// results (no matching tool_use in retained history) are preserved as text
// on the user message content rather than silently dropped, matching
// kiro-lb's orphaned-tool-result repair behavior.
func TestFilterOrphanedToolResults_PreservesText(t *testing.T) {
	history := []KiroHistoryMessage{
		{
			AssistantResponseMessage: &KiroAssistantResponseMessage{
				Content: "assistant",
				ToolUses: []KiroToolUse{
					{ToolUseID: "keep-1", Name: "Read", Input: map[string]interface{}{}},
				},
			},
		},
		{
			UserInputMessage: &KiroUserInputMessage{
				Content: "user-with-mixed-results",
				UserInputMessageContext: &KiroUserInputMessageContext{
					ToolResults: []KiroToolResult{
						{ToolUseID: "keep-1", Status: "success", Content: []KiroTextContent{{Text: "ok"}}},
						{ToolUseID: "orphan-1", Status: "success", Content: []KiroTextContent{{Text: "orphaned content"}}},
					},
				},
			},
		},
	}

	currentUserMsg := &KiroUserInputMessage{Content: "current"}
	currentToolResults := []KiroToolResult{
		{ToolUseID: "orphan-3", Status: "success", Content: []KiroTextContent{{Text: "current orphan"}}},
	}

	filteredHistory, filteredCurrentMsg, filteredCurrent := filterOrphanedToolResults(history, currentUserMsg, currentToolResults)

	// The matched tool result stays structured.
	ctx := filteredHistory[1].UserInputMessage.UserInputMessageContext
	if ctx == nil || len(ctx.ToolResults) != 1 || ctx.ToolResults[0].ToolUseID != "keep-1" {
		t.Fatalf("expected matched tool result to remain structured, got: %+v", ctx)
	}

	// The orphaned tool result text is preserved on the user message content.
	content := filteredHistory[1].UserInputMessage.Content
	if !contains(content, "orphaned content") {
		t.Fatalf("expected orphaned tool result text preserved on history user content, got: %q", content)
	}
	if !contains(content, "Tool Result (orphan-1)") {
		t.Fatalf("expected orphaned tool result marker in history content, got: %q", content)
	}

	// Current message orphan is preserved as text on current content.
	if !contains(filteredCurrentMsg.Content, "current orphan") {
		t.Fatalf("expected current orphaned tool result text preserved, got: %q", filteredCurrentMsg.Content)
	}
	if len(filteredCurrent) != 0 {
		t.Fatalf("expected current orphaned tool results removed from structured list, got: %+v", filteredCurrent)
	}
}

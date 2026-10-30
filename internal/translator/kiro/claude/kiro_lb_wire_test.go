package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

// Ported from kiro-lb src/convert_core.rs (807c8ab): the generation payload
// announces the agent mode at the top level and roots the conversation state.
func TestBuildKiroPayloadSendsAgentModeAndRootConversation(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	result, _ := BuildKiroPayload(body, "model", "", "AI_EDITOR", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AgentMode != "vibe" {
		t.Fatalf("agentMode = %q, want %q", payload.AgentMode, "vibe")
	}
	state := payload.ConversationState
	if state.RootConversationID == "" {
		t.Fatal("rootConversationId is empty")
	}
	if state.RootConversationID != state.ConversationID {
		t.Fatalf("rootConversationId = %q, conversationId = %q", state.RootConversationID, state.ConversationID)
	}
	if state.AgentContinuationID == "" {
		t.Fatal("agentContinuationId is empty")
	}
}

// Ported from kiro-lb src/stream_core.rs (807c8ab) StreamError::MalformedToolInput:
// an unparsable tool input that is not a truncation fails the turn instead of
// reaching the client as a tool call with empty arguments.
func TestProcessToolUseEventRejectsMalformedToolInput(t *testing.T) {
	t.Parallel()

	_, _, err := ProcessToolUseEvent(map[string]any{
		"toolUseId": "tool_1",
		"name":      "Write",
		"input":     "this is not json",
		"stop":      true,
	}, nil, map[string]bool{})
	if err == nil {
		t.Fatal("expected malformed tool input error")
	}
	if !strings.Contains(err.Error(), "malformed tool input") {
		t.Fatalf("error = %v, want malformed tool input", err)
	}
}

func TestProcessToolUseEventKeepsTruncatedToolInputAsTruncation(t *testing.T) {
	t.Parallel()

	toolUses, _, err := ProcessToolUseEvent(map[string]any{
		"toolUseId": "tool_2",
		"name":      "Write",
		"input":     `{"file_path": `,
		"stop":      true,
	}, nil, map[string]bool{})
	if err != nil {
		t.Fatalf("truncated input returned error: %v", err)
	}
	if len(toolUses) != 1 {
		t.Fatalf("tool uses = %d, want 1", len(toolUses))
	}
	if !toolUses[0].IsTruncated {
		t.Fatal("truncated tool use was not marked truncated")
	}
}

func TestProcessToolUseEventAcceptsEmptyInput(t *testing.T) {
	t.Parallel()

	toolUses, _, err := ProcessToolUseEvent(map[string]any{
		"toolUseId": "tool_3",
		"name":      "TaskList",
		"input":     "",
		"stop":      true,
	}, nil, map[string]bool{})
	if err != nil {
		t.Fatalf("empty input returned error: %v", err)
	}
	if len(toolUses) != 1 {
		t.Fatalf("tool uses = %d, want 1", len(toolUses))
	}
}

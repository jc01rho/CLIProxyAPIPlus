package claude

import (
	"encoding/json"
	"testing"
)

// TestClaudeSignedReasoningForwarding verifies that assistant messages
// carrying thinking blocks with a signature are forwarded through Kiro's
// nested reasoningContent field, matching kiro-lb
// extract_reasoning_signature_from_anthropic_content + build_kiro_history.
func TestClaudeSignedReasoningForwarding(t *testing.T) {
	input := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1000,
		"messages": [
			{"role": "user", "content": "First"},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "I reasoned step by step", "signature": "sig-xyz789"},
					{"type": "text", "text": "Answer"}
				]
			},
			{"role": "user", "content": "Next"}
		]
	}`)

	result, _ := BuildKiroPayload(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	for _, h := range payload.ConversationState.History {
		if h.AssistantResponseMessage == nil {
			continue
		}
		asst := h.AssistantResponseMessage
		if asst.ReasoningContent == nil {
			t.Fatalf("Expected assistant history message to carry reasoningContent, got: %+v", asst)
		}
		if asst.ReasoningContent.ReasoningText.Text != "I reasoned step by step" {
			t.Errorf("Expected reasoning text forwarded, got: %q", asst.ReasoningContent.ReasoningText.Text)
		}
		if asst.ReasoningContent.ReasoningText.Signature != "sig-xyz789" {
			t.Errorf("Expected reasoning signature forwarded, got: %q", asst.ReasoningContent.ReasoningText.Signature)
		}
		return
	}
	t.Fatal("No assistant history message found")
}

// TestClaudeUnsignedThinkingNotForwarded verifies that a thinking block
// without a signature is not forwarded through the nested field.
func TestClaudeUnsignedThinkingNotForwarded(t *testing.T) {
	input := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1000,
		"messages": [
			{"role": "user", "content": "First"},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "unsigned thinking"},
					{"type": "text", "text": "Answer"}
				]
			},
			{"role": "user", "content": "Next"}
		]
	}`)

	result, _ := BuildKiroPayload(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	for _, h := range payload.ConversationState.History {
		if h.AssistantResponseMessage != nil && h.AssistantResponseMessage.ReasoningContent != nil {
			t.Fatalf("Expected NO reasoningContent for unsigned thinking, got: %+v", h.AssistantResponseMessage.ReasoningContent)
		}
	}
}

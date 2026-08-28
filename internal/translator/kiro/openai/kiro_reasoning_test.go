package openai

import (
	"encoding/json"
	"testing"
)

// TestSignedReasoningForwarding verifies that assistant messages carrying a
// reasoning_signature are forwarded through Kiro's nested reasoningContent
// field, matching kiro-lb build_kiro_history. Kiro enforces the signature
// (THINKING_SIGNATURE_INVALID on empty/fabricated values), so unsigned
// reasoning is deliberately not forwarded.
func TestSignedReasoningForwarding(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "First"},
			{
				"role": "assistant",
				"content": "Answer",
				"reasoning_content": "I thought step by step",
				"reasoning_signature": "sig-abc123"
			},
			{"role": "user", "content": "Next"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Find the assistant history message carrying the signature.
	for _, h := range payload.ConversationState.History {
		if h.AssistantResponseMessage == nil {
			continue
		}
		asst := h.AssistantResponseMessage
		if asst.ReasoningContent == nil {
			t.Fatalf("Expected assistant history message to carry reasoningContent, got: %+v", asst)
		}
		if asst.ReasoningContent.ReasoningText.Text != "I thought step by step" {
			t.Errorf("Expected reasoning text forwarded, got: %q", asst.ReasoningContent.ReasoningText.Text)
		}
		if asst.ReasoningContent.ReasoningText.Signature != "sig-abc123" {
			t.Errorf("Expected reasoning signature forwarded, got: %q", asst.ReasoningContent.ReasoningText.Signature)
		}
		return
	}
	t.Fatal("No assistant history message found")
}

// TestUnsignedReasoningNotForwarded verifies that reasoning without a
// signature is dropped (not folded into the nested field), matching kiro-lb
// which only forwards a client-supplied signature.
func TestUnsignedReasoningNotForwarded(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "First"},
			{
				"role": "assistant",
				"content": "Answer",
				"reasoning_content": "unsigned thinking"
			},
			{"role": "user", "content": "Next"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	for _, h := range payload.ConversationState.History {
		if h.AssistantResponseMessage != nil && h.AssistantResponseMessage.ReasoningContent != nil {
			t.Fatalf("Expected NO reasoningContent for unsigned reasoning, got: %+v", h.AssistantResponseMessage.ReasoningContent)
		}
	}
}

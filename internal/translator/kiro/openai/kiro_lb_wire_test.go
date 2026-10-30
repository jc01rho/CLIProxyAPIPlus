package openai

import (
	"encoding/json"
	"testing"
)

// Ported from kiro-lb src/convert_core.rs (807c8ab): the generation payload
// announces the agent mode at the top level and roots the conversation state.
func TestBuildKiroPayloadFromSendsAgentModeAndRootConversation(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	result, _ := BuildKiroPayloadFromOpenAI(body, "model", "", "AI_EDITOR", false, false, nil, nil)

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

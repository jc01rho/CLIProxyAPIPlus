package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAISyntheticAlternationTurnsAreEmpty(t *testing.T) {
	history := []KiroHistoryMessage{
		{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "opening"}},
		{UserInputMessage: &KiroUserInputMessage{Content: "first"}},
		{UserInputMessage: &KiroUserInputMessage{Content: "second"}},
	}
	history = ensureFirstMessageIsUserHistory(history, "model", "AI_EDITOR")
	history = ensureAlternatingHistory(history)

	if history[0].UserInputMessage == nil || history[0].UserInputMessage.Content != "" {
		t.Fatalf("leading synthetic user = %+v, want empty content", history[0])
	}
	foundEmptyAssistant := false
	for i := 1; i < len(history)-1; i++ {
		if history[i-1].UserInputMessage != nil && history[i].AssistantResponseMessage != nil && history[i+1].UserInputMessage != nil && history[i].AssistantResponseMessage.Content == "" {
			foundEmptyAssistant = true
		}
	}
	if !foundEmptyAssistant {
		t.Fatal("expected an empty synthetic assistant between consecutive users")
	}

	body := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"done"}]}`)
	result, _ := BuildKiroPayloadFromOpenAI(body, "model", "", "AI_EDITOR", false, false, nil, nil)
	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ConversationState.CurrentMessage.UserInputMessage.Content != "" {
		t.Fatalf("assistant continuation content = %q, want empty", payload.ConversationState.CurrentMessage.UserInputMessage.Content)
	}
}

func TestOpenAIToolResultsOnlyMatchImmediatelyPrecedingAssistantRun(t *testing.T) {
	history := []KiroHistoryMessage{
		{AssistantResponseMessage: &KiroAssistantResponseMessage{ToolUses: []KiroToolUse{{ToolUseID: "old", Name: "Read"}}}},
		{UserInputMessage: &KiroUserInputMessage{Content: "intervening user"}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "later assistant"}},
	}
	current := &KiroUserInputMessage{Content: "current"}
	results := []KiroToolResult{{ToolUseID: "old", Status: "success", Content: []KiroTextContent{{Text: "old result"}}}}

	_, current, results = filterOrphanedToolResults(history, current, results)
	if len(results) != 0 {
		t.Fatalf("non-adjacent tool results remained structured: %+v", results)
	}
	if !strings.Contains(current.Content, "old result") {
		t.Fatalf("non-adjacent tool result was not preserved as text: %q", current.Content)
	}
}

func TestOpenAISystemPromptMovesToFirstHistoryUser(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"system","content":"historical instruction"},
			{"role":"user","content":"first question"},
			{"role":"assistant","content":"first answer"},
			{"role":"user","content":"current question"}
		]
	}`)
	result, _ := BuildKiroPayloadFromOpenAI(body, "model", "", "AI_EDITOR", false, false, nil, nil)
	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.ConversationState.History) == 0 || payload.ConversationState.History[0].UserInputMessage == nil {
		t.Fatalf("missing first user history: %+v", payload.ConversationState.History)
	}
	if !strings.Contains(payload.ConversationState.History[0].UserInputMessage.Content, "historical instruction") {
		t.Fatalf("system prompt missing from first history user: %q", payload.ConversationState.History[0].UserInputMessage.Content)
	}
	if strings.Contains(payload.ConversationState.CurrentMessage.UserInputMessage.Content, "historical instruction") {
		t.Fatalf("system prompt remained on current message: %q", payload.ConversationState.CurrentMessage.UserInputMessage.Content)
	}
}

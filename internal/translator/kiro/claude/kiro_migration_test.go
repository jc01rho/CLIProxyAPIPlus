package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeSyntheticAlternationTurnsAreEmpty(t *testing.T) {
	messages := gjson.Parse(`[
		{"role":"assistant","content":"opening"},
		{"role":"user","content":"current"}
	]`)
	history, _, _ := processMessages(messages, "model", "AI_EDITOR", true)
	if len(history) == 0 || history[0].UserInputMessage == nil {
		t.Fatalf("missing leading synthetic user: %+v", history)
	}
	if history[0].UserInputMessage.Content != "" {
		t.Fatalf("leading synthetic user content = %q, want empty", history[0].UserInputMessage.Content)
	}

	history = ensureAlternatingHistory([]KiroHistoryMessage{
		{UserInputMessage: &KiroUserInputMessage{Content: "first"}},
		{UserInputMessage: &KiroUserInputMessage{Content: "second"}},
	})
	if len(history) != 3 || history[1].AssistantResponseMessage == nil {
		t.Fatalf("alternated history = %+v", history)
	}
	if history[1].AssistantResponseMessage.Content != "" {
		t.Fatalf("synthetic assistant content = %q, want empty", history[1].AssistantResponseMessage.Content)
	}
}

func TestClaudeToolResultsOnlyMatchImmediatelyPrecedingAssistantRun(t *testing.T) {
	body := []byte(`{
		"tools":[{"name":"Read","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","content":[{"type":"tool_use","id":"old","name":"Read","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"first result"}]},
			{"role":"assistant","content":"later assistant"},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"stale result"}]}
		]
	}`)
	result, _ := BuildKiroPayload(body, "model", "", "AI_EDITOR", false, false, nil, nil)
	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	if current.UserInputMessageContext != nil && len(current.UserInputMessageContext.ToolResults) != 0 {
		t.Fatalf("non-adjacent tool result remained structured: %+v", current.UserInputMessageContext.ToolResults)
	}
	if !strings.Contains(current.Content, "stale result") {
		t.Fatalf("non-adjacent tool result was not preserved as text: %q", current.Content)
	}
}

func TestClaudeSystemPromptMovesToFirstHistoryUser(t *testing.T) {
	body := []byte(`{
		"system":"historical instruction",
		"messages":[
			{"role":"user","content":"first question"},
			{"role":"assistant","content":"first answer"},
			{"role":"user","content":"current question"}
		]
	}`)
	result, _ := BuildKiroPayload(body, "model", "", "AI_EDITOR", false, false, nil, nil)
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

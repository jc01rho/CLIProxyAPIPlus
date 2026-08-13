package openai

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToCursorReusesMatchingModelPayload(t *testing.T) {
	input := []byte(`{"model":"composer-2","messages":[{"role":"user","content":"hello"}]}`)

	output := ConvertOpenAIRequestToCursor("composer-2", input, false)

	if &output[0] != &input[0] {
		t.Fatal("matching model caused a payload copy")
	}
}

func TestConvertOpenAIRequestToCursorUpdatesDifferentModel(t *testing.T) {
	input := []byte(`{"model":"old-model","messages":[]}`)

	output := ConvertOpenAIRequestToCursor("composer-2", input, false)

	if model := gjson.GetBytes(output, "model").String(); model != "composer-2" {
		t.Fatalf("model = %q, want composer-2", model)
	}
}

func TestFlattenConversationIntoUserTextKeepsToolResultsInUserMessage(t *testing.T) {
	input := []byte(`{
		"model":"composer-2",
		"messages":[
			{"role":"system","content":"Be brief."},
			{"role":"user","content":"list files"},
			{"role":"assistant","content":"calling tool"},
			{"role":"tool","tool_call_id":"call_1","content":"README.md"},
			{"role":"user","content":"summarize"}
		]
	}`)

	output := FlattenConversationIntoUserText(input)
	messages := gjson.GetBytes(output, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2 (system + flattened user)", len(messages))
	}
	if messages[0].Get("role").String() != "system" {
		t.Fatalf("first role = %q, want system", messages[0].Get("role").String())
	}
	user := messages[1].Get("content").String()
	for _, want := range []string{"USER: list files", "ASSISTANT: calling tool", "TOOL_RESULT (call_id: call_1): README.md", "Current request: summarize"} {
		if !strings.Contains(user, want) {
			t.Fatalf("flattened user missing %q; got %q", want, user)
		}
	}
}

func TestConvertOpenAIRequestToCursorDoesNotFlattenToolMessages(t *testing.T) {
	input := []byte(`{
		"model":"composer-2",
		"messages":[
			{"role":"user","content":"list files"},
			{"role":"tool","tool_call_id":"call_1","content":"README.md"}
		]
	}`)

	output := ConvertOpenAIRequestToCursor("composer-2", input, false)
	if gjson.GetBytes(output, "messages.#").Int() != 2 {
		t.Fatalf("request translator must keep structured tool messages for session resume")
	}
}

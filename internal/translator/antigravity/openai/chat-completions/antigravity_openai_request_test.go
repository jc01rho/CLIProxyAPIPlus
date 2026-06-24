package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

<<<<<<< HEAD
func TestConvertOpenAIRequestToAntigravityPadsMissingToolResponses(t *testing.T) {
	input := []byte(`{
		"messages": [
			{"role":"user","content":"call tools"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"path\":\"/tmp/a\"}"}},
				{"id":"call_2","type":"function","function":{"name":"Grep","arguments":"{\"pattern\":\"x\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"read ok"}
		]
	}`)

	out := ConvertOpenAIRequestToAntigravity("gemini-2.5-pro", input, false)

	callCount := len(gjson.GetBytes(out, "request.contents.1.parts").Array())
	responseCount := len(gjson.GetBytes(out, "request.contents.2.parts").Array())
	if callCount != 2 || responseCount != 2 {
		t.Fatalf("expected 2 function calls and 2 responses, got calls=%d responses=%d body=%s", callCount, responseCount, out)
	}
	if got := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.name").String(); got != "Grep" {
		t.Fatalf("expected second response to use second call name, got %q", got)
	}
}

func TestConvertOpenAIRequestToAntigravityUsesSameFallbackNameForEmptyToolName(t *testing.T) {
	input := []byte(`{
		"messages": [
			{"role":"user","content":"call tool"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		]
	}`)

	out := ConvertOpenAIRequestToAntigravity("gemini-2.5-pro", input, false)

	callName := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.name").String()
	responseName := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String()
	if callName == "" || callName != responseName {
		t.Fatalf("expected fallback call/response names to match, got call=%q response=%q body=%s", callName, responseName, out)
=======
func TestConvertOpenAIRequestToAntigravitySkipsEmptyTextPartsWithoutNulls(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": ""},
					{"type": "input_audio", "input_audio": {"data": "SUQzBA==", "format": "mp3"}}
				]
			},
			{
				"role": "assistant",
				"content": [{"type": "text", "text": ""}],
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "read_file", "arguments": "{\"path\":\"a.txt\"}"}
				}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "{\"output\":\"ok\"}"},
			{"role": "user", "content": "done"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	userParts := gjson.GetBytes(result, "request.contents.0.parts").Array()
	if len(userParts) != 1 {
		t.Fatalf("user parts length = %d, want 1. Output: %s", len(userParts), result)
	}
	if userParts[0].Type == gjson.Null {
		t.Fatalf("user parts.0 is null. Output: %s", result)
	}
	if got := userParts[0].Get("inlineData.mime_type").String(); got != "audio/mpeg" {
		t.Fatalf("audio mime_type = %q, want audio/mpeg. Output: %s", got, result)
	}

	assistantParts := gjson.GetBytes(result, "request.contents.1.parts").Array()
	if len(assistantParts) != 1 {
		t.Fatalf("assistant parts length = %d, want 1. Output: %s", len(assistantParts), result)
	}
	if assistantParts[0].Type == gjson.Null {
		t.Fatalf("assistant parts.0 is null. Output: %s", result)
	}
	if !assistantParts[0].Get("functionCall").Exists() {
		t.Fatalf("functionCall missing. Output: %s", result)
>>>>>>> upstream/main
	}
}

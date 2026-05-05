package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGeminiCLIPadsMissingToolResponses(t *testing.T) {
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

	out := ConvertOpenAIRequestToGeminiCLI("gemini-2.5-pro", input, false)

	callCount := len(gjson.GetBytes(out, "request.contents.1.parts").Array())
	responseCount := len(gjson.GetBytes(out, "request.contents.2.parts").Array())
	if callCount != 2 || responseCount != 2 {
		t.Fatalf("expected 2 function calls and 2 responses, got calls=%d responses=%d body=%s", callCount, responseCount, out)
	}
	if got := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.name").String(); got != "Grep" {
		t.Fatalf("expected second response to use second call name, got %q", got)
	}
}

func TestConvertOpenAIRequestToGeminiCLIUsesSameFallbackNameForEmptyToolName(t *testing.T) {
	input := []byte(`{
		"messages": [
			{"role":"user","content":"call tool"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		]
	}`)

	out := ConvertOpenAIRequestToGeminiCLI("gemini-2.5-pro", input, false)

	callName := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.name").String()
	responseName := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String()
	if callName == "" || callName != responseName {
		t.Fatalf("expected fallback call/response names to match, got call=%q response=%q body=%s", callName, responseName, out)
	}
}

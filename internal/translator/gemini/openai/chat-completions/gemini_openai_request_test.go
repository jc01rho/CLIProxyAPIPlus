package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGeminiPadsMissingToolResponses(t *testing.T) {
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

	out := ConvertOpenAIRequestToGemini("gemini-2.5-pro", input, false)

	callCount := len(gjson.GetBytes(out, "contents.1.parts").Array())
	responseCount := len(gjson.GetBytes(out, "contents.2.parts").Array())
	if callCount != 2 || responseCount != 2 {
		t.Fatalf("expected 2 function calls and 2 responses, got calls=%d responses=%d body=%s", callCount, responseCount, out)
	}
	if got := gjson.GetBytes(out, "contents.2.parts.1.functionResponse.name").String(); got != "Grep" {
		t.Fatalf("expected second response to use second call name, got %q", got)
	}
}

func TestConvertOpenAIRequestToGeminiUsesSameFallbackNameForEmptyToolName(t *testing.T) {
	input := []byte(`{
		"messages": [
			{"role":"user","content":"call tool"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		]
	}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.5-pro", input, false)

	callName := gjson.GetBytes(out, "contents.1.parts.0.functionCall.name").String()
	responseName := gjson.GetBytes(out, "contents.2.parts.0.functionResponse.name").String()
	if callName == "" || callName != responseName {
		t.Fatalf("expected fallback call/response names to match, got call=%q response=%q body=%s", callName, responseName, out)
	}
}

func TestConvertOpenAIRequestToGemini_StripsTrailingAssistantPrefill(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.4",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "previous answer"}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	contents := resultJSON.Get("contents").Array()

	if len(contents) != 1 {
		t.Fatalf("contents length = %d, want 1. contents=%s", len(contents), resultJSON.Get("contents").Raw)
	}
	if got := contents[0].Get("role").String(); got != "user" {
		t.Fatalf("final remaining role = %q, want %q", got, "user")
	}
}

func TestConvertOpenAIRequestToGeminiPreservesInputAudio(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.5",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Transcribe this audio verbatim."},
					{"type": "input_audio", "input_audio": {"data": "SUQzBA==", "format": "mp3"}}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	parts := resultJSON.Get("contents.0.parts").Array()

	if len(parts) != 2 {
		t.Fatalf("parts length = %d, want 2. parts=%s", len(parts), resultJSON.Get("contents.0.parts").Raw)
	}
	if got := parts[0].Get("text").String(); got != "Transcribe this audio verbatim." {
		t.Fatalf("text part = %q, want prompt text", got)
	}
	if got := parts[1].Get("inlineData.mime_type").String(); got != "audio/mpeg" {
		t.Fatalf("audio mime_type = %q, want %q", got, "audio/mpeg")
	}
	if got := parts[1].Get("inlineData.data").String(); got != "SUQzBA==" {
		t.Fatalf("audio data = %q, want %q", got, "SUQzBA==")
	}
}

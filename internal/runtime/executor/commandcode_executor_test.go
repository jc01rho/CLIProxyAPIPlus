package executor

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func Test_BuildCommandCodePayload_serializes_message_content_as_strings(t *testing.T) {
	// Given
	payload := []byte(`{
		"model": "parrot",
		"messages": [
			{"role": "system", "content": "system instructions"},
			{"role": "user", "content": [
				{"type": "text", "text": "hello"},
				{"type": "text", "text": "world"},
				{"type": "image_url", "image_url": {"url": "https://example.test/image.png"}}
			]},
			{"role": "assistant", "content": "plain answer"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "lookup", "arguments": "{\"query\":\"sparrow\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "tool output"}
		]
	}`)

	// When
	got, err := buildCommandCodePayload(payload, "nvidia/nemotron-3-ultra-550b-a55b", false)

	// Then
	if err != nil {
		t.Fatalf("buildCommandCodePayload() error = %v", err)
	}
	if got := gjson.GetBytes(got, "params.system").String(); got != "system instructions" {
		t.Fatalf("params.system = %q, want %q", got, "system instructions")
	}

	messages := gjson.GetBytes(got, "params.messages").Array()
	if len(messages) != 3 {
		t.Fatalf("len(params.messages) = %d, want 3", len(messages))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want %q", got, "user")
	}
	assertCommandCodeMessageContent(t, messages[0], "hello\nworld")
	assertCommandCodeMessageContent(t, messages[1], "plain answer")
	assertCommandCodeMessageContent(t, messages[2], "tool call lookup {\"query\":\"sparrow\"}")

	var envelope struct {
		Params struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		} `json:"params"`
	}
	if err := json.Unmarshal(got, &envelope); err != nil {
		t.Fatalf("unmarshal envelope with string message content: %v", err)
	}
}

func assertCommandCodeMessageContent(t *testing.T, message gjson.Result, want string) {
	t.Helper()
	if got := message.Get("content").Type; got != gjson.String {
		t.Fatalf("message content type = %v, want %v; raw=%s", got, gjson.String, message.Raw)
	}
	if got := message.Get("content").String(); got != want {
		t.Fatalf("message content = %q, want %q", got, want)
	}
}

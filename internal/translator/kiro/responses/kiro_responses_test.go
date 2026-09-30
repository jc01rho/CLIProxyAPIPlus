package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesRequestConvertsTypedInputAdditionalToolsAndEffort(t *testing.T) {
	chat, err := ResponsesRequestToChat([]byte(`{
		"model":"gpt-5.6-sol","instructions":"be concise","stream":true,
		"reasoning":{"effort":"ultra"},
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]},
			{"type":"additional_tools","tools":[{"type":"namespace","tools":[{"type":"function","name":"read","description":"read a file","parameters":{"type":"object"}}]}]}
		],
		"tools":[{"type":"custom","name":"shell","description":"execute","format":{"syntax":"shell"}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	root := gjson.ParseBytes(chat)
	if root.Get("messages.0.role").String() != "system" || root.Get("messages.1.content").String() != "inspect" {
		t.Fatalf("messages = %s", chat)
	}
	if root.Get("reasoning_effort").String() != "max" || !root.Get("stream").Bool() {
		t.Fatalf("reasoning/stream = %s", chat)
	}
	if root.Get("tools.#").Int() != 2 || root.Get("tools.0.function.name").String() != "shell" || root.Get("tools.0.function.parameters.required.0").String() != "input" {
		t.Fatalf("custom bridge = %s", chat)
	}
	if root.Get("tools.1.function.name").String() != "read" {
		t.Fatalf("additional_tools missing = %s", chat)
	}
}

func TestResponsesRequestRejectsPreviousResponseID(t *testing.T) {
	_, err := ResponsesRequestToChat([]byte(`{"previous_response_id":"resp_old","input":"continue"}`))
	if err == nil || !strings.Contains(err.Error(), "previous_response_id is not supported") {
		t.Fatalf("error = %v", err)
	}
	if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != 400 {
		t.Fatalf("error does not carry 400: %T %v", err, err)
	}
}

func TestKiroResponsesNonStreamEnvelopeAndCustomToolRoundTrip(t *testing.T) {
	request := []byte(`{"model":"kiro-model","input":"run","tools":[{"type":"custom","name":"shell"}]}`)
	response := []byte(`{
		"id":"msg_kiro","model":"kiro-model","stop_reason":"tool_use",
		"content":[{"type":"tool_use","id":"call_shell","name":"shell","input":{"input":"ls -la"}}],
		"usage":{"input_tokens":3,"output_tokens":4}
	}`)
	out := ConvertKiroNonStreamToOpenAIResponses(context.Background(), "kiro-model", request, nil, response, nil)
	root := gjson.ParseBytes(out)
	if root.Get("object").String() != "response" || root.Get("status").String() != "completed" {
		t.Fatalf("envelope = %s", out)
	}
	if item := root.Get("output.0"); item.Get("type").String() != "custom_tool_call" || item.Get("input").String() != "ls -la" || item.Get("call_id").String() != "call_shell" {
		t.Fatalf("custom output = %s", out)
	}
	if root.Get("usage.input_tokens").Int() != 3 || root.Get("usage.output_tokens").Int() != 4 {
		t.Fatalf("usage = %s", out)
	}
}

func TestKiroResponsesStreamEmitsItemCenteredSequence(t *testing.T) {
	request := []byte(`{"model":"kiro-model","input":"hello"}`)
	var state any
	var events []string
	for _, raw := range [][]byte{
		[]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"kiro-model\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}"),
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}"),
		[]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}"),
		[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}"),
	} {
		for _, event := range ConvertKiroStreamToOpenAIResponses(context.Background(), "kiro-model", request, nil, raw, &state) {
			events = append(events, gjson.GetBytes(event, "type").String())
		}
	}
	want := []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.completed"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v, want %v", events, want)
	}
}

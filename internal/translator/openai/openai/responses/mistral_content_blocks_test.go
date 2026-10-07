package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Mistral returns message.content as an array of ThinkChunk/TextChunk blocks when
// reasoning is enabled (reasoning_effort=high). Without normalization the raw JSON
// array leaks into the visible output_text; these tests pin the split into a
// reasoning item plus visible text.

func TestSplitContentBlocksArray(t *testing.T) {
	t.Parallel()

	arrayContent := gjson.Parse(`[{"type":"thinking","thinking":[{"type":"text","text":"secret "},{"type":"text","text":"thought"}],"closed":true},{"type":"text","text":"OK"}]`)
	reasoning, visible, isArray := splitContentBlocksArray(arrayContent)
	if !isArray {
		t.Fatalf("isArray = false, want true")
	}
	if reasoning != "secret thought" {
		t.Fatalf("reasoning = %q, want %q", reasoning, "secret thought")
	}
	if visible != "OK" {
		t.Fatalf("visible = %q, want %q", visible, "OK")
	}

	if _, _, isArray := splitContentBlocksArray(gjson.Parse(`"plain string"`)); isArray {
		t.Fatalf("plain string reported as array")
	}

	thinkingOnly := gjson.Parse(`[{"type":"thinking","thinking":"raw thought","closed":true}]`)
	reasoning, visible, isArray = splitContentBlocksArray(thinkingOnly)
	if !isArray || reasoning != "raw thought" || visible != "" {
		t.Fatalf("thinking-only split = (%q, %q, %v)", reasoning, visible, isArray)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_MistralContentArray(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"id":"chatcmpl_mistral","object":"chat.completion","created":1773896263,"model":"mistral-large-4","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"secret thought"}],"closed":true},{"type":"text","text":"OK"}],"tool_calls":null},"finish_reason":"stop"}]}`)

	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "mistral-large-4", nil, nil, raw, nil)
	data := gjson.ParseBytes(out)

	var reasoningText, messageText string
	var reasoningFound, messageFound bool
	data.Get("output").ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "reasoning":
			reasoningFound = true
			reasoningText = item.Get("summary.0.text").String()
		case "message":
			messageFound = true
			messageText = item.Get("content.0.text").String()
		}
		return true
	})

	if !reasoningFound {
		t.Fatalf("no reasoning item; out=%s", out)
	}
	if reasoningText != "secret thought" {
		t.Fatalf("reasoning summary = %q, want %q; out=%s", reasoningText, "secret thought", out)
	}
	if !messageFound {
		t.Fatalf("no message item; out=%s", out)
	}
	if messageText != "OK" {
		t.Fatalf("message text = %q, want %q; out=%s", messageText, "OK", out)
	}
	if strings.Contains(messageText, `"type":"thinking"`) {
		t.Fatalf("raw thinking JSON leaked into message text: %q", messageText)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_MistralContentArrayStream(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"mistral-large-4"}`)
	chunks := []string{
		`data: {"id":"chatcmpl_mistral","object":"chat.completion.chunk","created":1773896263,"model":"mistral-large-4","choices":[{"index":0,"delta":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"secret thought"}],"closed":true}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_mistral","object":"chat.completion.chunk","created":1773896263,"model":"mistral-large-4","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"OK"}]},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}

	var param any
	var reasoningDelta, visibleDelta strings.Builder
	var reasoningAdded, messageAdded int

	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "mistral-large-4", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				switch data.Get("item.type").String() {
				case "reasoning":
					reasoningAdded++
				case "message":
					messageAdded++
				}
			case "response.reasoning_summary_text.delta":
				reasoningDelta.WriteString(data.Get("delta").String())
			case "response.output_text.delta":
				visibleDelta.WriteString(data.Get("delta").String())
			}
		}
	}

	if reasoningAdded != 1 {
		t.Fatalf("reasoning output_item.added = %d, want 1", reasoningAdded)
	}
	if messageAdded != 1 {
		t.Fatalf("message output_item.added = %d, want 1", messageAdded)
	}
	if reasoningDelta.String() != "secret thought" {
		t.Fatalf("reasoning delta = %q, want %q", reasoningDelta.String(), "secret thought")
	}
	if visibleDelta.String() != "OK" {
		t.Fatalf("visible delta = %q, want %q", visibleDelta.String(), "OK")
	}
	if strings.Contains(visibleDelta.String(), `"type":"thinking"`) {
		t.Fatalf("raw thinking JSON leaked into visible output: %q", visibleDelta.String())
	}
}

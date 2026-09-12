package openai

import (
	"context"
	"strings"
	"testing"

	responsesconverter "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
)

// TestChatToResponsesEmitsTerminalEventWithoutDoneMarker pins that a chat
// stream which closes without the [DONE] marker still yields a terminal
// response event. The converter defers response.completed until [DONE] so
// late usage-only chunks can populate usage; without the marker the stream
// previously ended after response.created/in_progress and clients reported
// "stream ended before a terminal response event".
func TestChatToResponsesEmitsTerminalEventWithoutDoneMarker(t *testing.T) {
	ctx := context.Background()
	req := []byte(`{"model":"swe-2-high","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	var param any
	var all []string

	// One content chunk, then the upstream goes away without [DONE].
	chunk := []byte(`{"id":"chatcmpl-devin","object":"chat.completion.chunk","created":0,"model":"swe-2-high","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`)
	for _, out := range responsesconverter.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "swe-2-high", req, req, chunk, &param) {
		all = append(all, string(out))
	}
	if strings.Contains(strings.Join(all, "\n"), "response.completed") {
		t.Fatal("precondition failed: completed emitted before the done marker")
	}

	// WriteDone feeds the marker so the turn terminates.
	for _, out := range responsesconverter.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "swe-2-high", req, req, []byte("[DONE]"), &param) {
		all = append(all, string(out))
	}
	joined := strings.Join(all, "\n")
	if !strings.Contains(joined, "response.completed") {
		t.Fatalf("no terminal event after the done marker:\n%s", joined)
	}

	// Feeding it twice must not emit a second terminal event.
	extra := responsesconverter.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "swe-2-high", req, req, []byte("[DONE]"), &param)
	for _, out := range extra {
		if strings.Contains(string(out), "response.completed") {
			t.Fatal("duplicate terminal event on repeated done marker")
		}
	}
}

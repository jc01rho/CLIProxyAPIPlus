package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// grok-4.6 (and other models routed through Cursor's H2 stream) previously
// had its reasoning wrapped in literal <think>...</think> tags inside the
// OpenAI delta's content field. OpenAI-compatible clients (e.g. pi-ai) only
// recognize reasoning_content/reasoning/reasoning_text as reasoning and have
// no support for parsing inline think tags out of content, so those tags
// leaked into the user-visible answer verbatim and confused downstream tool
// call parsing. cursorTextDeltaJSON must emit reasoning as a dedicated
// reasoning_content delta field instead.
func TestCursorTextDeltaJSONSeparatesReasoningFromContent(t *testing.T) {
	roleSent := false

	thinkingDelta := cursorTextDeltaJSON("let me think", true, &roleSent)
	if !gjson.Valid(thinkingDelta) {
		t.Fatalf("thinking delta is not valid JSON: %s", thinkingDelta)
	}
	if got := gjson.Get(thinkingDelta, "role").String(); got != "assistant" {
		t.Fatalf("first delta role = %q, want %q", got, "assistant")
	}
	if got := gjson.Get(thinkingDelta, "reasoning_content").String(); got != "let me think" {
		t.Fatalf("reasoning_content = %q, want %q", got, "let me think")
	}
	if gjson.Get(thinkingDelta, "content").Exists() {
		t.Fatalf("thinking delta must not also set content: %s", thinkingDelta)
	}
	for _, tag := range []string{"<think>", "</think>"} {
		if strings.Contains(thinkingDelta, tag) {
			t.Fatalf("thinking delta must never contain literal %q tag: %s", tag, thinkingDelta)
		}
	}

	answerDelta := cursorTextDeltaJSON("The answer is 4.", false, &roleSent)
	if !gjson.Valid(answerDelta) {
		t.Fatalf("answer delta is not valid JSON: %s", answerDelta)
	}
	if gjson.Get(answerDelta, "role").Exists() {
		t.Fatalf("role must only be sent once; second delta re-sent it: %s", answerDelta)
	}
	if got := gjson.Get(answerDelta, "content").String(); got != "The answer is 4." {
		t.Fatalf("content = %q, want %q", got, "The answer is 4.")
	}
	if gjson.Get(answerDelta, "reasoning_content").Exists() {
		t.Fatalf("answer delta must not also set reasoning_content: %s", answerDelta)
	}
	for _, tag := range []string{"<think>", "</think>"} {
		if strings.Contains(answerDelta, tag) {
			t.Fatalf("answer delta must never contain literal %q tag: %s", tag, answerDelta)
		}
	}
}

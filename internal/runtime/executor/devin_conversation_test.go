package executor

import (
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestDevinRequestTextPreservesConversation pins that a multi-turn request keeps
// every turn. The old implementation assigned text = m.content per user message,
// so only the final user message survived and all prior context (including the
// user's real question when a trailing notice block followed it) was dropped.
func TestDevinRequestTextPreservesConversation(t *testing.T) {
	payload := []byte(`{"model":"swe-2-high","messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"한글로 나에게 말해봐"},
		{"role":"assistant","content":"네, 한글로 답변드리겠습니다."},
		{"role":"user","content":"내일 날씨를 말해줘"}
	]}`)

	text, system := devinRequestText(cliproxyexecutor.Request{Payload: payload})

	if system != "You are helpful." {
		t.Fatalf("system = %q, want %q", system, "You are helpful.")
	}
	for _, want := range []string{"한글로 나에게 말해봐", "내일 날씨를 말해줘", "네, 한글로 답변드리겠습니다."} {
		if !strings.Contains(text, want) {
			t.Fatalf("conversation text lost %q\ngot: %s", want, text)
		}
	}
}

// TestDevinRequestTextJoinsMultipleSystemPrompts pins that several system blocks
// are all forwarded instead of only the first.
func TestDevinRequestTextJoinsMultipleSystemPrompts(t *testing.T) {
	payload := []byte(`{"model":"swe-2-high","messages":[
		{"role":"system","content":"Always answer in Korean."},
		{"role":"system","content":"Be concise."},
		{"role":"user","content":"hi"}
	]}`)

	_, system := devinRequestText(cliproxyexecutor.Request{Payload: payload})
	for _, want := range []string{"Always answer in Korean.", "Be concise."} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt lost %q\ngot: %s", want, system)
		}
	}
}

// TestDevinRequestTextSingleTurnStaysBare keeps the common single-turn shape
// free of role labels so simple prompts are forwarded verbatim.
func TestDevinRequestTextSingleTurnStaysBare(t *testing.T) {
	payload := []byte(`{"model":"swe-2-high","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"just this"}
	]}`)

	text, _ := devinRequestText(cliproxyexecutor.Request{Payload: payload})
	if text != "just this" {
		t.Fatalf("single-turn text = %q, want %q", text, "just this")
	}
}

// buildDevinProbeFrame encodes one length-delimited protobuf field.
func buildDevinProbeFrame(fieldNum int, text string) []byte {
	out := []byte{byte(fieldNum<<3 | 2)}
	out = append(out, byte(len(text)))
	return append(out, []byte(text)...)
}

// TestDevinExtractSeparatesAnswerFromReasoning pins the field split observed on
// the live API: f3 carries the user-visible answer, f9 the private reasoning.
// Reading f9 as the answer leaked chain-of-thought and dropped the real reply.
func TestDevinExtractSeparatesAnswerFromReasoning(t *testing.T) {
	answerFrame := buildDevinProbeFrame(devinAnswerField, "Hello!")
	reasoningFrame := buildDevinProbeFrame(devinReasoningField, "The user wants a greeting.")

	if got, ok := devinExtractTextDelta(answerFrame); !ok || got != "Hello!" {
		t.Fatalf("answer delta = (%q,%v), want (\"Hello!\",true)", got, ok)
	}
	if got, ok := devinExtractTextDelta(reasoningFrame); ok {
		t.Fatalf("reasoning frame must not surface as answer, got %q", got)
	}
	if got, ok := devinExtractReasoningDelta(reasoningFrame); !ok || got != "The user wants a greeting." {
		t.Fatalf("reasoning delta = (%q,%v)", got, ok)
	}
	if _, ok := devinExtractReasoningDelta(answerFrame); ok {
		t.Fatal("answer frame must not surface as reasoning")
	}
}

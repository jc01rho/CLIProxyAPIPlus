package executor

import (
	"encoding/json"
	"testing"
)

func decodeMistral(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v (payload=%s)", err, payload)
	}
	return got
}

func mistralMessage(t *testing.T, got map[string]any, container string) map[string]any {
	t.Helper()
	choices, ok := got["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("missing choices: %v", got)
	}
	choice := choices[0].(map[string]any)
	msg, ok := choice[container].(map[string]any)
	if !ok {
		t.Fatalf("missing %s: %v", container, choice)
	}
	return msg
}

func TestNormalizeMistralArrayContentNonStream(t *testing.T) {
	payload := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Think A"}],"closed":true},{"type":"text","text":"HELLO"}]}}]}`)
	got := decodeMistral(t, normalizeMistralArrayContent(payload, false))
	msg := mistralMessage(t, got, "message")
	if msg["content"] != "HELLO" {
		t.Fatalf("content = %v, want HELLO", msg["content"])
	}
	if msg["reasoning_content"] != "Think A" {
		t.Fatalf("reasoning_content = %v, want Think A", msg["reasoning_content"])
	}
}

func TestNormalizeMistralArrayContentStreamThinkingOnly(t *testing.T) {
	payload := []byte(`{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"only thought"}],"closed":true}]}}]}`)
	got := decodeMistral(t, normalizeMistralArrayContent(payload, true))
	msg := mistralMessage(t, got, "delta")
	if msg["content"] != "" {
		t.Fatalf("content = %v, want empty string", msg["content"])
	}
	if msg["reasoning_content"] != "only thought" {
		t.Fatalf("reasoning_content = %v, want only thought", msg["reasoning_content"])
	}
}

func TestNormalizeMistralArrayContentJoinsMultipleParts(t *testing.T) {
	payload := []byte(`{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"closed":true},{"type":"text","text":"x"},{"type":"text","text":"y"}]}}]}`)
	got := decodeMistral(t, normalizeMistralArrayContent(payload, true))
	msg := mistralMessage(t, got, "delta")
	if msg["content"] != "xy" {
		t.Fatalf("content = %v, want xy", msg["content"])
	}
	if msg["reasoning_content"] != "ab" {
		t.Fatalf("reasoning_content = %v, want ab", msg["reasoning_content"])
	}
}

func TestNormalizeMistralArrayContentLeavesStringContent(t *testing.T) {
	payload := []byte(`{"choices":[{"index":0,"delta":{"content":"plain text"}}]}`)
	got := decodeMistral(t, normalizeMistralArrayContent(payload, true))
	msg := mistralMessage(t, got, "delta")
	if msg["content"] != "plain text" {
		t.Fatalf("content = %v, want plain text", msg["content"])
	}
	if _, exists := msg["reasoning_content"]; exists {
		t.Fatalf("unexpected reasoning_content: %v", msg["reasoning_content"])
	}
}

func TestNormalizeMistralStreamLine(t *testing.T) {
	line := []byte(`data: {"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"t"}],"closed":true},{"type":"text","text":"hi"}]}}]}`)
	out := normalizeMistralStreamLine(line)
	trimmed := out
	for len(trimmed) > 0 && trimmed[0] == ' ' {
		trimmed = trimmed[1:]
	}
	if string(trimmed[:5]) != "data:" {
		t.Fatalf("missing data prefix: %s", out)
	}
	got := decodeMistral(t, trimmed[5:])
	msg := mistralMessage(t, got, "delta")
	if msg["content"] != "hi" || msg["reasoning_content"] != "t" {
		t.Fatalf("content=%v reasoning_content=%v", msg["content"], msg["reasoning_content"])
	}
}

func TestNormalizeMistralStreamLinePassthrough(t *testing.T) {
	for _, line := range [][]byte{[]byte("data: [DONE]"), []byte(": keep-alive"), []byte("")} {
		if got := normalizeMistralStreamLine(line); string(got) != string(line) {
			t.Fatalf("line %q changed to %q", line, got)
		}
	}
}

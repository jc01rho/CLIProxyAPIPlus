package executor

import (
	"strings"
	"testing"
)

// TestCommandCodeNormalizeCallID is a table-driven test of the pure
// normalization helper: short IDs pass through unchanged, long IDs are
// hashed down to a deterministic <=64-char value, and the same input always
// produces the same output (required for assistant/tool-result pairing).
func TestCommandCodeNormalizeCallID(t *testing.T) {
	longID80 := strings.Repeat("a", 80)
	longID65 := strings.Repeat("b", 65)
	shortID := "call_abc123"
	exactly64 := strings.Repeat("c", 64)

	tests := []struct {
		name string
		id   string
	}{
		{name: "short id unchanged", id: shortID},
		{name: "exactly 64 chars unchanged", id: exactly64},
		{name: "80 char id normalized", id: longID80},
		{name: "65 char id normalized", id: longID65},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commandCodeNormalizeCallID(tt.id)
			if len(got) > commandCodeMaxCallIDLen {
				t.Fatalf("commandCodeNormalizeCallID(%q) = %q (len %d), want len <= %d", tt.id, got, len(got), commandCodeMaxCallIDLen)
			}
			if len(tt.id) <= commandCodeMaxCallIDLen {
				if got != tt.id {
					t.Fatalf("commandCodeNormalizeCallID(%q) = %q, want unchanged", tt.id, got)
				}
			}
			// Determinism: same input always normalizes to the same output.
			again := commandCodeNormalizeCallID(tt.id)
			if again != got {
				t.Fatalf("commandCodeNormalizeCallID(%q) not deterministic: %q vs %q", tt.id, got, again)
			}
		})
	}

	// Distinct long IDs must not collide (sanity check, not a collision proof).
	normA := commandCodeNormalizeCallID(longID80)
	normB := commandCodeNormalizeCallID(longID65)
	if normA == normB {
		t.Fatalf("distinct long IDs normalized to the same value: %q", normA)
	}
}

// TestCommandCodeLongToolCallIDNormalizedOnWire verifies an 80-char tool call
// ID exceeding the CommandCode 64-char limit is normalized to <=64 chars on
// the wire tool-call block.
func TestCommandCodeLongToolCallIDNormalizedOnWire(t *testing.T) {
	longID := strings.Repeat("x", 80)

	payload := []byte(`{
		"model": "higher-coding",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"` + longID + `","type":"function","function":{"name":"good_tool","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"` + longID + `","content":"result"}
		],
		"tools":[{"type":"function","function":{"name":"good_tool","description":"d","parameters":{}}}]
	}`)

	out, err := buildCommandCodePayload(payload, "higher-coding", false)
	if err != nil {
		t.Fatalf("buildCommandCodePayload returned error: %v", err)
	}

	toolCalls, toolResults := collectCommandCodeWireBlocks(t, out)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call block, got %d: %v", len(toolCalls), toolCalls)
	}
	if len(toolResults) != 1 {
		t.Fatalf("expected 1 tool result block, got %d: %v", len(toolResults), toolResults)
	}
	if len(toolCalls[0]) > commandCodeMaxCallIDLen {
		t.Fatalf("wire tool-call id %q exceeds %d chars (len %d)", toolCalls[0], commandCodeMaxCallIDLen, len(toolCalls[0]))
	}
	if toolCalls[0] == longID {
		t.Fatalf("wire tool-call id was not normalized, still the raw 80-char id")
	}
}

// TestCommandCodeLongToolCallIDStaysPairedWithResult verifies that when both
// the assistant tool-call and the subsequent tool-result message carry the
// same over-length ID, normalization preserves pairing: both wire blocks end
// up with the identical (now-shortened) toolCallId.
func TestCommandCodeLongToolCallIDStaysPairedWithResult(t *testing.T) {
	longID := strings.Repeat("p", 90)

	payload := []byte(`{
		"model": "higher-coding",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"` + longID + `","type":"function","function":{"name":"good_tool","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"` + longID + `","content":"result"},
			{"role":"assistant","content":"final"}
		],
		"tools":[{"type":"function","function":{"name":"good_tool","description":"d","parameters":{}}}]
	}`)

	out, err := buildCommandCodePayload(payload, "higher-coding", false)
	if err != nil {
		t.Fatalf("buildCommandCodePayload returned error: %v", err)
	}

	toolCalls, toolResults := collectCommandCodeWireBlocks(t, out)
	if len(toolCalls) != 1 || len(toolResults) != 1 {
		t.Fatalf("expected exactly 1 tool call and 1 tool result, got calls=%v results=%v", toolCalls, toolResults)
	}
	if toolCalls[0] != toolResults[0] {
		t.Fatalf("tool-call id %q and tool-result id %q diverged after normalization; pairing broken", toolCalls[0], toolResults[0])
	}
	// Confirm it matches the direct helper output too.
	want := commandCodeNormalizeCallID(longID)
	if toolCalls[0] != want {
		t.Fatalf("wire tool-call id = %q, want %q", toolCalls[0], want)
	}
}

// TestCommandCodeShortToolCallIDUnchanged verifies IDs already within the
// 64-char limit are passed through byte-for-byte on the wire.
func TestCommandCodeShortToolCallIDUnchanged(t *testing.T) {
	shortID := "call_short_id_123"

	payload := []byte(`{
		"model": "higher-coding",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"` + shortID + `","type":"function","function":{"name":"good_tool","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"` + shortID + `","content":"result"}
		],
		"tools":[{"type":"function","function":{"name":"good_tool","description":"d","parameters":{}}}]
	}`)

	out, err := buildCommandCodePayload(payload, "higher-coding", false)
	if err != nil {
		t.Fatalf("buildCommandCodePayload returned error: %v", err)
	}

	toolCalls, toolResults := collectCommandCodeWireBlocks(t, out)
	if len(toolCalls) != 1 || toolCalls[0] != shortID {
		t.Fatalf("expected tool-call id unchanged %q, got %v", shortID, toolCalls)
	}
	if len(toolResults) != 1 || toolResults[0] != shortID {
		t.Fatalf("expected tool-result id unchanged %q, got %v", shortID, toolResults)
	}
}

// TestCommandCodeEmptyToolCallIDStillErrors verifies the pre-existing
// empty-ID validation still fires unchanged; normalization must not mask a
// truly empty tool-call id.
func TestCommandCodeEmptyToolCallIDStillErrors(t *testing.T) {
	payload := []byte(`{
		"model": "higher-coding",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"","type":"function","function":{"name":"good_tool","arguments":"{}"}}
			]}
		],
		"tools":[{"type":"function","function":{"name":"good_tool","description":"d","parameters":{}}}]
	}`)

	_, err := buildCommandCodePayload(payload, "higher-coding", false)
	if err == nil {
		t.Fatalf("expected an error for empty tool-call id, got nil")
	}
}

// TestCommandCodeEmptyToolCallMessageIDStillErrors verifies a tool-role
// message with an empty tool_call_id still errors unchanged.
func TestCommandCodeEmptyToolCallMessageIDStillErrors(t *testing.T) {
	payload := []byte(`{
		"model": "higher-coding",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"tool","tool_call_id":"","content":"result"}
		]
	}`)

	_, err := buildCommandCodePayload(payload, "higher-coding", false)
	if err == nil {
		t.Fatalf("expected an error for empty tool_call_id message, got nil")
	}
}

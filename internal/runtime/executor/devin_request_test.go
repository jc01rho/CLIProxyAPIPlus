package executor

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"
)

// devinFindField returns the payload of the first matching top-level field.
func devinFindField(buf []byte, want int) ([]byte, bool) {
	var out []byte
	found := false
	devinScanFields(buf, func(num, wire int, _ uint64, data []byte) bool {
		if num == want && wire == 2 {
			out = data
			found = true
			return false
		}
		return true
	})
	return out, found
}

// TestDevinFullRequestCarriesEveryField pins the complete request layout:
// metadata, per-turn prompts, request type, completion config, tools,
// cascade id, model and prompt id.
func TestDevinFullRequestCarriesEveryField(t *testing.T) {
	spec := devinChatSpec{
		Token:    "tok",
		ModelUID: "swe-2-high",
		System:   "be brief",
		Messages: []devinMessage{
			{role: "user", content: "hello"},
			{role: "assistant", content: "hi"},
		},
		SessionUUID: "sess-1",
		CascadeID:   "casc-1",
		PromptID:    "prompt-1",
		TriggerID:   "trig-1",
		RequestID:   42,
		Config:      devinDefaultCompletionConfig(),
		Now:         time.Unix(1700000000, 0),
	}
	body := devinBuildChatRequestFull(spec)[5:]

	if _, ok := devinFindField(body, devinReqMetadataField); !ok {
		t.Fatal("metadata field missing")
	}
	if _, ok := devinFindField(body, devinReqConfigField); !ok {
		t.Fatal("completion configuration missing")
	}
	for _, want := range []string{"casc-1", "prompt-1", "swe-2-high", "be brief"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("request missing %q", want)
		}
	}

	// Each conversation turn must ride its own prompt submessage.
	prompts := 0
	devinScanFields(body, func(num, wire int, _ uint64, _ []byte) bool {
		if num == devinReqPromptField && wire == 2 {
			prompts++
		}
		return true
	})
	if prompts != 2 {
		t.Fatalf("prompt submessages = %d, want 2", prompts)
	}
}

// TestDevinMetadataCarriesIdentityFields pins the metadata submessage.
func TestDevinMetadataCarriesIdentityFields(t *testing.T) {
	meta := devinBuildMetadata(devinChatSpec{
		Token: "tok", SessionUUID: "sess-1", TriggerID: "trig-1",
		RequestID: 7, UserJWT: "jwt-1", Now: time.Unix(1700000000, 0),
	})
	for _, want := range []string{"tok", "sess-1", "trig-1", "jwt-1", "Unset"} {
		if !bytes.Contains(meta, []byte(want)) {
			t.Fatalf("metadata missing %q", want)
		}
	}
	if _, ok := devinFindField(meta, devinMetaTimestampField); !ok {
		t.Fatal("timestamp submessage missing")
	}
}

// TestDevinCompletionConfigFromPayload pins sampling parameters being taken
// from the incoming request instead of always using defaults.
func TestDevinCompletionConfigFromPayload(t *testing.T) {
	cfg := devinExtractCompletionConfig([]byte(`{"temperature":0.1,"top_p":0.5,"max_tokens":999}`))
	if cfg.Temperature != 0.1 || cfg.TopP != 0.5 || cfg.MaxOutputTokens != 999 {
		t.Fatalf("config = %+v", cfg)
	}
	def := devinExtractCompletionConfig([]byte(`{}`))
	if def.Temperature != 0.7 || def.TopK != 50 {
		t.Fatalf("defaults = %+v", def)
	}
}

// TestDevinEncodeDoubleRoundTrips guards the fixed64 encoding used for
// temperature and top_p.
func TestDevinEncodeDoubleRoundTrips(t *testing.T) {
	enc := devinEncodeDouble(5, 0.7)
	if len(enc) != 9 {
		t.Fatalf("length = %d, want 9", len(enc))
	}
	got := math.Float64frombits(binary.LittleEndian.Uint64(enc[1:]))
	if got != 0.7 {
		t.Fatalf("decoded = %v, want 0.7", got)
	}
}

// TestDevinToolResultRoundTripEncoding pins that an assistant tool call and
// the following tool result keep their linkage, which the agent loop needs.
func TestDevinToolResultRoundTripEncoding(t *testing.T) {
	msgs, system := devinRequestMessages([]byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"weather?"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"sunny"}]}`))
	if system != "sys" {
		t.Fatalf("system = %q", system)
	}
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}
	if len(msgs[1].toolCalls) != 1 || msgs[1].toolCalls[0].ID != "call_1" {
		t.Fatalf("assistant tool call lost: %+v", msgs[1])
	}
	if msgs[2].toolCallID != "call_1" {
		t.Fatalf("tool result id lost: %+v", msgs[2])
	}

	enc := devinEncodeMessagePrompt("sess", msgs[2])
	if !bytes.Contains(enc, []byte("call_1")) {
		t.Fatal("tool_call_id not encoded")
	}
	var source uint64
	devinScanFields(enc, func(num, wire int, v uint64, _ []byte) bool {
		if num == devinPromptSourceField && wire == 0 {
			source = v
			return false
		}
		return true
	})
	if source != devinSourceTool {
		t.Fatalf("tool source = %d, want %d", source, devinSourceTool)
	}
}

// TestDevinSourceForRole pins the role mapping, including that a system
// message never uses source 3, which the upstream rejects.
func TestDevinSourceForRole(t *testing.T) {
	cases := map[string]int{"user": devinSourceUser, "assistant": devinSourceAssistant, "tool": devinSourceTool, "system": devinSourceUser}
	for role, want := range cases {
		if got := devinSourceForRole(role); got != want {
			t.Fatalf("devinSourceForRole(%q) = %d, want %d", role, got, want)
		}
	}
}

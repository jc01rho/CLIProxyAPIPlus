package executor

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// devinFieldFrame builds one length-delimited protobuf field.
func devinFieldFrame(num int, payload []byte) []byte {
	out := devinEncodeVarint(nil, uint64(num<<3|2))
	out = devinEncodeVarint(out, uint64(len(payload)))
	return append(out, payload...)
}

func devinVarintFrame(num int, v uint64) []byte {
	out := devinEncodeVarint(nil, uint64(num<<3|0))
	return devinEncodeVarint(out, v)
}

// devinFloatEntry builds a usage entry: {#5 metric, #4{#2 float32}}.
func devinFloatEntry(metric string, value float32) []byte {
	var val [4]byte
	binary.LittleEndian.PutUint32(val[:], math.Float32bits(value))
	inner := devinEncodeVarint(nil, uint64(2<<3|5))
	inner = append(inner, val[:]...)

	var entry []byte
	entry = append(entry, devinFieldFrame(4, inner)...)
	entry = append(entry, devinFieldFrame(5, []byte(metric))...)
	return entry
}

// TestDevinToolsAreSentOnField10 pins that tool definitions are encoded into
// request field 10, matching the published protocol layout. Previously no
// tools were transmitted at all, so agent models announced an action and then
// terminated without ever issuing a call.
func TestDevinToolsAreSentOnField10(t *testing.T) {
	tools := []devinToolDef{{
		Name:        "get_weather",
		Description: "Look up the weather",
		Parameters:  []byte(`{"type":"object","properties":{"city":{"type":"string"}}}`),
	}}
	body := devinBuildChatRequest("tok", "swe-2-high", "sys", "hi", "sess", "", tools...)

	if !bytes.Contains(body, []byte("get_weather")) {
		t.Fatal("tool name missing from encoded request")
	}
	if !bytes.Contains(body, []byte("Look up the weather")) {
		t.Fatal("tool description missing from encoded request")
	}

	// The tool submessage must sit on field 10.
	payload := body[5:]
	var found bool
	devinScanFields(payload, func(num, wire int, _ uint64, data []byte) bool {
		if num == devinReqToolsField && wire == 2 && bytes.Contains(data, []byte("get_weather")) {
			found = true
			return false
		}
		return true
	})
	if !found {
		t.Fatalf("tool definition not encoded on field %d", devinReqToolsField)
	}
}

// TestDevinRequestWithoutToolsHasNoToolField keeps plain chat requests byte
// identical to the previous behaviour.
func TestDevinRequestWithoutToolsHasNoToolField(t *testing.T) {
	body := devinBuildChatRequest("tok", "swe-2-high", "sys", "hi", "sess", "")
	devinScanFields(body[5:], func(num, wire int, _ uint64, _ []byte) bool {
		if num == devinReqToolsField {
			t.Fatal("no tools were requested but field 10 was emitted")
		}
		return true
	})
}

// TestDevinExtractToolCallDelta pins ToolCallDelta parsing on field 6.
func TestDevinExtractToolCallDelta(t *testing.T) {
	var inner []byte
	inner = append(inner, devinFieldFrame(devinToolCallIDField, []byte("call_1"))...)
	inner = append(inner, devinFieldFrame(devinToolCallNameField, []byte("get_weather"))...)
	inner = append(inner, devinFieldFrame(devinToolCallArgsField, []byte(`{"city":`))...)
	frame := devinFieldFrame(devinToolCallField, inner)

	call, ok := devinExtractToolCallDelta(frame)
	if !ok {
		t.Fatal("expected a tool call delta")
	}
	if call.ID != "call_1" || call.Name != "get_weather" || call.Arguments != `{"city":` {
		t.Fatalf("unexpected tool call: %+v", call)
	}
}

// TestDevinMergeToolCallAccumulatesArguments pins that streamed argument
// fragments are concatenated rather than overwriting one another.
func TestDevinMergeToolCallAccumulatesArguments(t *testing.T) {
	calls := make([]devinToolCall, 0, 2)
	byID := make(map[string]int, 2)
	devinMergeToolCall(&calls, byID, devinToolCall{ID: "c1", Name: "f", Arguments: `{"a":`})
	devinMergeToolCall(&calls, byID, devinToolCall{ID: "c1", Arguments: `1}`})

	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Arguments != `{"a":1}` {
		t.Fatalf("arguments = %q, want %q", calls[0].Arguments, `{"a":1}`)
	}
}

// TestDevinFinishReasonMapping pins the StopReason enum mapping, especially
// FUNCTION_CALL(10) which previously could never surface because the payload
// hardcoded "stop".
func TestDevinFinishReasonMapping(t *testing.T) {
	cases := map[uint64]string{
		0:  "stop",
		1:  "length",
		2:  "stop",
		3:  "length",
		10: "tool_calls",
		11: "content_filter",
	}
	for enum, want := range cases {
		if got := devinFinishReason(enum); got != want {
			t.Fatalf("devinFinishReason(%d) = %q, want %q", enum, got, want)
		}
		reason, ok := devinExtractFinishReason(devinVarintFrame(devinFinishReasonField, enum))
		if !ok || reason != want {
			t.Fatalf("extract(%d) = (%q,%v), want %q", enum, reason, ok, want)
		}
	}
}

// TestDevinExtractUsage pins usage parsing on field 28. Values are float32,
// which is why an earlier varint-only scan concluded Devin reported no tokens.
func TestDevinExtractUsage(t *testing.T) {
	var block []byte
	block = append(block, devinFieldFrame(2, devinFloatEntry("input_tokens", 1234))...)
	block = append(block, devinFieldFrame(2, devinFloatEntry("output_tokens", 56))...)
	block = append(block, devinFieldFrame(2, devinFloatEntry("cached_input_tokens", 900))...)
	frame := devinFieldFrame(devinUsageField, block)

	usage, ok := devinExtractUsage(frame)
	if !ok {
		t.Fatal("expected usage to be found")
	}
	if usage.PromptTokens != 1234 || usage.CompletionTokens != 56 || usage.CachedTokens != 900 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.Total() != 1290 {
		t.Fatalf("total = %d, want 1290", usage.Total())
	}
}

// TestDevinCompletionPayloadCarriesToolCallsAndUsage pins the emitted OpenAI
// body so tool calls and token counts reach the client.
func TestDevinCompletionPayloadCarriesToolCallsAndUsage(t *testing.T) {
	calls := []devinToolCall{{ID: "call_1", Name: "get_weather", Arguments: `{"city":"Seoul"}`}}
	usage := devinUsage{PromptTokens: 100, CompletionTokens: 20}
	got := string(devinBuildCompletionPayload("swe-2-high", "text", calls, "tool_calls", usage))

	for _, want := range []string{
		`"finish_reason":"tool_calls"`,
		`"name":"get_weather"`,
		`"prompt_tokens":100`,
		`"completion_tokens":20`,
		`"total_tokens":120`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("payload missing %s\ngot: %s", want, got)
		}
	}
}

// TestDevinCompletionPayloadWithoutUsageStaysEmpty keeps the previous shape
// when the upstream reports nothing.
func TestDevinCompletionPayloadWithoutUsageStaysEmpty(t *testing.T) {
	got := string(devinBuildCompletionPayload("m", "hi", nil, "stop", devinUsage{}))
	if !strings.Contains(got, `"usage":{}`) {
		t.Fatalf("expected empty usage, got: %s", got)
	}
	if strings.Contains(got, "tool_calls") {
		t.Fatalf("unexpected tool_calls: %s", got)
	}
}

// TestDevinExtractToolsFromPayload pins OpenAI tool definitions being read off
// the incoming request.
func TestDevinExtractToolsFromPayload(t *testing.T) {
	payload := []byte(`{"model":"swe-2-high","messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"get_weather","description":"d","parameters":{"type":"object"}}}]}`)
	tools := devinExtractTools(payload)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if tools[0].Name != "get_weather" {
		t.Fatalf("name = %q", tools[0].Name)
	}
}

// TestDevinNormalizeToolCallID pins that bare upstream ids gain the call_
// prefix the Responses API requires, while conforming ids are preserved.
func TestDevinNormalizeToolCallID(t *testing.T) {
	cases := map[string]string{
		"web_search_0": "call_web_search_0",
		"call_abc":     "call_abc",
		"":             "call_devin_3",
	}
	for in, want := range cases {
		if got := devinNormalizeToolCallID(in, 3); got != want {
			t.Fatalf("devinNormalizeToolCallID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDevinToolCallsJSONNormalizesIDs pins the emitted payload.
func TestDevinToolCallsJSONNormalizesIDs(t *testing.T) {
	got := devinToolCallsJSON([]devinToolCall{{ID: "web_search_0", Name: "web_search", Arguments: "{}"}})
	if !strings.Contains(got, `"id":"call_web_search_0"`) {
		t.Fatalf("id not normalized: %s", got)
	}
}

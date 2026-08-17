package proto

import (
	"bytes"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

type wireField struct {
	num    protowire.Number
	varint uint64
	data   []byte
	isVar  bool
}

// parseFields decodes a wire message into field-number → occurrences.
func parseFields(t *testing.T, buf []byte) map[protowire.Number][]wireField {
	t.Helper()
	out := make(map[protowire.Number][]wireField)
	for len(buf) > 0 {
		num, typ, n := protowire.ConsumeTag(buf)
		if n < 0 {
			t.Fatalf("bad tag: %v", protowire.ParseError(n))
		}
		buf = buf[n:]
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(buf)
			if m < 0 {
				t.Fatalf("bad varint field %d", num)
			}
			buf = buf[m:]
			out[num] = append(out[num], wireField{num: num, varint: v, isVar: true})
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(buf)
			if m < 0 {
				t.Fatalf("bad bytes field %d", num)
			}
			buf = buf[m:]
			out[num] = append(out[num], wireField{num: num, data: v})
		default:
			t.Fatalf("unexpected wire type %v for field %d", typ, num)
		}
	}
	return out
}

func TestEncodeRunRequestWireShape(t *testing.T) {
	p := &RunRequestParams{
		ModelId:      "composer-2.5",
		SystemPrompt: "You are a helpful assistant.",
		UserText:     "hello",
		Turns:        []TurnData{{UserText: "hi", AssistantText: "hey"}},
		McpTools: []McpToolDef{{
			Name:        "get_weather",
			Description: "Get weather",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
	}
	buf := EncodeRunRequest(p)

	acm := parseFields(t, buf)
	arrRaw, ok := acm[1]
	if !ok || len(arrRaw) != 1 {
		t.Fatalf("AgentClientMessage.run_request (field 1) missing: %v", acm)
	}
	arr := parseFields(t, arrRaw[0].data)

	// field 1: conversation_state present
	if _, ok := arr[1]; !ok {
		t.Fatal("conversation_state (field 1) missing")
	}

	// field 2: action → user_message_action → user_message placeholders
	ca := parseFields(t, arr[2][0].data)
	uma := parseFields(t, ca[1][0].data)
	um := parseFields(t, uma[1][0].data)
	if _, ok := um[2]; !ok || len(um[2][0].data) == 0 {
		t.Fatal("UserMessage.message_id (field 2) missing/empty")
	}
	if _, ok := um[3]; !ok {
		t.Fatal("UserMessage.selected_context (field 3) envelope missing")
	}
	if len(um[4]) == 0 || !um[4][0].isVar || um[4][0].varint != 1 {
		t.Fatalf("UserMessage.mode (field 4) must be varint 1, got %v", um[4])
	}

	// field 3: model_details uses resolved id
	md := parseFields(t, arr[3][0].data)
	if got := string(md[1][0].data); got != "composer-2.5" {
		t.Fatalf("model_details.model_id = %q, want composer-2.5", got)
	}

	// field 4: mcp_tools envelope present (non-empty: 1 tool)
	if _, ok := arr[4]; !ok {
		t.Fatal("mcp_tools (field 4) envelope missing")
	}
	mcp := parseFields(t, arr[4][0].data)
	if len(mcp[1]) != 1 {
		t.Fatalf("mcp_tools entry count = %d, want 1", len(mcp[1]))
	}

	// field 5: conversation_id present
	if len(arr[5][0].data) == 0 {
		t.Fatal("conversation_id (field 5) empty")
	}

	// field 9: requested_model must be absent for an unparameterized model --
	// opencodex (cursor-agent CLI reference) only sends requested_model when
	// explicit model parameters exist; sending it alongside model_details for a
	// plain model triggers a conflicting-selection "Connect not_found" upstream.
	if _, ok := arr[9]; ok {
		t.Fatalf("requested_model (field 9) must be absent for unparameterized model, got %v", arr[9])
	}

	// fields 12 and 16 do not exist in the real AgentRunRequest schema
	// (agent.v1.AgentRunRequest, msg 91 -- only fields 1-9 are defined per
	// opencodex's generated agent_pb.ts) and must never be sent.
	if _, ok := arr[12]; ok {
		t.Fatalf("field 12 does not exist in AgentRunRequest and must not be sent, got %v", arr[12])
	}
	if _, ok := arr[16]; ok {
		t.Fatalf("field 16 does not exist in AgentRunRequest and must not be sent, got %v", arr[16])
	}
}

func TestEncodeRunRequestEmptyPlaceholders(t *testing.T) {
	p := &RunRequestParams{ModelId: "composer-2.5", UserText: "hi"}
	buf := EncodeRunRequest(p)
	acm := parseFields(t, buf)
	arr := parseFields(t, acm[1][0].data)

	// mcp_tools envelope must be present even when empty
	if _, ok := arr[4]; !ok {
		t.Fatal("mcp_tools (field 4) envelope must be present when no tools")
	}
	if len(arr[4][0].data) != 0 {
		t.Fatalf("empty mcp_tools envelope must encode to 0 bytes, got %d", len(arr[4][0].data))
	}

	// selected_context envelope present (empty) inside UserMessage
	ca := parseFields(t, arr[2][0].data)
	uma := parseFields(t, ca[1][0].data)
	um := parseFields(t, uma[1][0].data)
	if _, ok := um[3]; !ok || len(um[3][0].data) != 0 {
		t.Fatal("selected_context (field 3) must be an empty envelope when no images")
	}
}

func TestEncodeRunRequestCheckpointUsesRawBytes(t *testing.T) {
	p := &RunRequestParams{
		ModelId:        "composer-2.5",
		UserText:       "continue",
		ConversationId: "conv-123",
		RawCheckpoint:  []byte{0x0a, 0x01, 0xff},
	}
	buf := EncodeRunRequest(p)
	acm := parseFields(t, buf)
	arr := parseFields(t, acm[1][0].data)
	if !bytes.Equal(arr[1][0].data, p.RawCheckpoint) {
		t.Fatal("conversation_state must be the raw checkpoint")
	}
	if got := string(arr[5][0].data); got != "conv-123" {
		t.Fatalf("conversation_id = %q, want conv-123", got)
	}
}

func TestResolveRequestedModel(t *testing.T) {
	cases := []struct {
		in     string
		wantID string
		params []ModelParameter
	}{
		{"", "composer-2.5", nil},
		{"auto", "default", nil},
		{"auto-cost", "default", []ModelParameter{{ID: "optimization", Value: "cost"}}},
		{"auto-balance", "default", []ModelParameter{{ID: "optimization", Value: "balance"}}},
		{"composer-2.5", "composer-2.5", nil},
		{"composer-2-5", "composer-2.5", nil},
		{"composer-2.5-sdk", "composer-2.5", nil},
		{"composer-2.5-fast", "composer-2.5", []ModelParameter{{ID: "fast", Value: "true"}}},
		{"composer-2-5-fast", "composer-2.5", []ModelParameter{{ID: "fast", Value: "true"}}},
		{"claude-sonnet-4.6-high", "claude-4.6-sonnet-medium", nil},
		{"claude-4.6-opus", "claude-4.6-opus-max", nil},
		{"claude-4.6-opus-high", "claude-4.6-opus-high", nil},
		{"gpt-5.2-xhigh", "gpt-5.2-xhigh", nil},
		{"gpt-5.2", "gpt-5.2-xhigh", nil},
		{"grok-4.5", "cursor-grok-4.5-high", nil},
		{"grok-4.5-fast", "grok-4.5", []ModelParameter{{ID: "effort", Value: "high"}, {ID: "fast", Value: "true"}}},
		{"some-custom-model", "some-custom-model", nil},
	}
	for _, c := range cases {
		gotID, gotParams := ResolveRequestedModel(c.in, "")
		if gotID != c.wantID {
			t.Errorf("ResolveRequestedModel(%q) id = %q, want %q", c.in, gotID, c.wantID)
		}
		if len(gotParams) != len(c.params) {
			t.Errorf("ResolveRequestedModel(%q) params = %v, want %v", c.in, gotParams, c.params)
			continue
		}
		for i := range gotParams {
			if gotParams[i] != c.params[i] {
				t.Errorf("ResolveRequestedModel(%q) params[%d] = %v, want %v", c.in, i, gotParams[i], c.params[i])
			}
		}
	}
}

func TestResolveRequestedModelReasoningEffort(t *testing.T) {
	cases := []struct {
		in, effort, wantID string
		params             []ModelParameter
	}{
		{"claude-4.6-opus", "high", "claude-4.6-opus-high", nil},
		{"grok-4.5", "low", "cursor-grok-4.5-low", nil},
		{"grok-4.5-fast", "medium", "grok-4.5", []ModelParameter{{ID: "effort", Value: "medium"}, {ID: "fast", Value: "true"}}},
		{"composer-2.5", "high", "composer-2.5", nil},
	}
	for _, c := range cases {
		gotID, gotParams := ResolveRequestedModel(c.in, c.effort)
		if gotID != c.wantID {
			t.Errorf("ResolveRequestedModel(%q, %q) id = %q, want %q", c.in, c.effort, gotID, c.wantID)
		}
		if len(gotParams) != len(c.params) {
			t.Errorf("ResolveRequestedModel(%q, %q) params = %v, want %v", c.in, c.effort, gotParams, c.params)
			continue
		}
		for i := range gotParams {
			if gotParams[i] != c.params[i] {
				t.Errorf("ResolveRequestedModel(%q, %q) params[%d] = %v, want %v", c.in, c.effort, i, gotParams[i], c.params[i])
			}
		}
	}
}

func TestEncodeRunRequestOmitsModelDetailsWhenParameterized(t *testing.T) {
	plain := parseFields(t, EncodeRunRequest(&RunRequestParams{ModelId: "composer-2.5", UserText: "hi"}))
	param := parseFields(t, EncodeRunRequest(&RunRequestParams{ModelId: "composer-2.5-fast", UserText: "hi"}))
	plainARR := parseFields(t, plain[1][0].data)
	paramARR := parseFields(t, param[1][0].data)
	if _, ok := plainARR[3]; !ok {
		t.Fatal("unparameterized request must include AgentRunRequest.model_details")
	}
	if _, ok := paramARR[3]; ok {
		t.Fatal("parameterized request must omit AgentRunRequest.model_details")
	}
	if _, ok := paramARR[9]; !ok {
		t.Fatal("parameterized request must still include requested_model")
	}
}

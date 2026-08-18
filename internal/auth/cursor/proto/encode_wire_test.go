package proto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

	// field 2: action → user_message_action → user_message
	ca := parseFields(t, arr[2][0].data)
	uma := parseFields(t, ca[1][0].data)
	um := parseFields(t, uma[1][0].data)
	if _, ok := um[2]; !ok || len(um[2][0].data) == 0 {
		t.Fatal("UserMessage.message_id (field 2) missing/empty")
	}
	// senpi parity: no request_context on the user action itself.
	if _, ok := uma[2]; ok {
		t.Fatal("UserMessageAction.request_context must be absent")
	}
	// No selected_context envelope or synthetic mode on text-only turns.
	if _, ok := um[3]; ok {
		t.Fatal("UserMessage.selected_context (field 3) must be absent when no images")
	}
	if _, ok := um[4]; ok {
		t.Fatal("UserMessage.mode (field 4) must be absent (proto3 default)")
	}
	// field 3: model_details uses the resolved id.
	md := parseFields(t, arr[3][0].data)
	if got := string(md[1][0].data); got != "composer-2.5" {
		t.Fatalf("model_details.model_id = %q, want composer-2.5", got)
	}
	if _, ok := md[5]; ok {
		t.Fatal("model_details.display_name_short must be absent")
	}

	// senpi does not advertise tools in AgentRunRequest.mcp_tools.
	if _, ok := arr[4]; ok {
		t.Fatal("mcp_tools (field 4) must be absent")
	}

	// senpi always includes requested_model, even when no parameters exist.
	if _, ok := arr[9]; !ok {
		t.Fatal("requested_model (field 9) missing")
	}

	// field 5: conversation_id present
	if len(arr[5][0].data) == 0 {
		t.Fatal("conversation_id (field 5) empty")
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

func TestEncodeRunRequestOmitsEmptyEnvelopes(t *testing.T) {
	p := &RunRequestParams{ModelId: "composer-2.5", UserText: "hi"}
	buf := EncodeRunRequest(p)
	acm := parseFields(t, buf)
	arr := parseFields(t, acm[1][0].data)

	// senpi parity: no mcp_tools envelope at all, even when tools are absent.
	if _, ok := arr[4]; ok {
		t.Fatal("mcp_tools (field 4) must be omitted when no tools")
	}

	ca := parseFields(t, arr[2][0].data)
	uma := parseFields(t, ca[1][0].data)
	if _, ok := uma[2]; ok {
		t.Fatal("UserMessageAction.request_context (field 2) must be omitted")
	}
	um := parseFields(t, uma[1][0].data)
	if _, ok := um[3]; ok {
		t.Fatal("selected_context (field 3) must be omitted when no images")
	}
}

func TestEncodeRunRequestUsesBlobReferencesForConversationTurns(t *testing.T) {
	p := &RunRequestParams{
		ModelId:            "composer-2.5",
		SystemPrompt:       "system",
		UserText:           "current",
		Turns:              []TurnData{{UserText: "prior user", AssistantText: "prior assistant"}},
		RootPromptMessages: [][]byte{[]byte(`{"role":"user","content":"prior user"}`)},
	}
	buf := EncodeRunRequest(p)
	arr := parseFields(t, parseFields(t, buf)[1][0].data)
	css := parseFields(t, arr[1][0].data)
	turns := css[8]
	if len(turns) != 1 {
		t.Fatalf("conversation_state.turns count = %d, want 1", len(turns))
	}
	turnID := turns[0].data
	if len(turnID) != sha256.Size {
		t.Fatalf("conversation turn reference length = %d, want %d", len(turnID), sha256.Size)
	}
	turnBytes, ok := p.BlobStore[hex.EncodeToString(turnID)]
	if !ok {
		t.Fatalf("conversation turn blob %x missing from BlobStore", turnID)
	}
	turn := parseFields(t, turnBytes)
	agentTurn := parseFields(t, turn[1][0].data)
	userID := agentTurn[1][0].data
	if len(userID) != sha256.Size {
		t.Fatalf("user message reference length = %d, want %d", len(userID), sha256.Size)
	}
	if _, ok := p.BlobStore[hex.EncodeToString(userID)]; !ok {
		t.Fatalf("user message blob %x missing from BlobStore", userID)
	}
	stepID := agentTurn[2][0].data
	if len(stepID) != sha256.Size {
		t.Fatalf("conversation step reference length = %d, want %d", len(stepID), sha256.Size)
	}
	if _, ok := p.BlobStore[hex.EncodeToString(stepID)]; !ok {
		t.Fatalf("conversation step blob %x missing from BlobStore", stepID)
	}
	rootIDs := css[1]
	if len(rootIDs) != 2 {
		t.Fatalf("root_prompt_messages_json count = %d, want system plus prior message", len(rootIDs))
	}
	rootMessageID := rootIDs[1].data
	if len(rootMessageID) != sha256.Size {
		t.Fatalf("root message reference length = %d, want %d", len(rootMessageID), sha256.Size)
	}
	if _, ok := p.BlobStore[hex.EncodeToString(rootMessageID)]; !ok {
		t.Fatalf("root message blob %x missing from BlobStore", rootMessageID)
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
		{"composer-2.5-fast", "composer-2.5-fast", nil},
		{"composer-2-5-fast", "composer-2.5-fast", nil},
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

func TestEncodeRunRequestIncludesBothModelSelectors(t *testing.T) {
	plain := parseFields(t, EncodeRunRequest(&RunRequestParams{ModelId: "composer-2.5", UserText: "hi"}))
	param := parseFields(t, EncodeRunRequest(&RunRequestParams{ModelId: "composer-2.5-fast", UserText: "hi"}))
	plainARR := parseFields(t, plain[1][0].data)
	paramARR := parseFields(t, param[1][0].data)
	if _, ok := plainARR[3]; !ok {
		t.Fatal("unparameterized request must include AgentRunRequest.model_details")
	}
	if _, ok := plainARR[9]; !ok {
		t.Fatal("unparameterized request must include AgentRunRequest.requested_model")
	}
	if _, ok := paramARR[3]; !ok {
		t.Fatal("parameterized request must include AgentRunRequest.model_details")
	}
	if _, ok := paramARR[9]; !ok {
		t.Fatal("parameterized request must include AgentRunRequest.requested_model")
	}
}

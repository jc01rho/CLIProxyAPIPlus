package proto

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// --- SelectedImage / SelectedContext encoding ---

func TestEncodeSelectedImageWireShape(t *testing.T) {
	buf := EncodeSelectedImage("uuid-1", "image/png", []byte{0x89, 0x50, 0x4e, 0x47})
	f := parseFields(t, buf)
	if got := string(f[SI_Uuid][0].data); got != "uuid-1" {
		t.Fatalf("SelectedImage.uuid = %q, want uuid-1", got)
	}
	if got := string(f[SI_MimeType][0].data); got != "image/png" {
		t.Fatalf("SelectedImage.mime_type = %q, want image/png", got)
	}
	if !bytes.Equal(f[SI_Data][0].data, []byte{0x89, 0x50, 0x4e, 0x47}) {
		t.Fatalf("SelectedImage.data = %x, want 89504e47", f[SI_Data][0].data)
	}
}

func TestEncodeRunRequestCarriesImagesInSelectedContext(t *testing.T) {
	png := []byte("png-bytes")
	jpeg := []byte("jpeg-bytes")
	p := &RunRequestParams{
		ModelId:  "composer-2.5",
		UserText: "what is in these?",
		Images: []ImageData{
			{MimeType: "image/png", Data: png},
			{MimeType: "image/jpeg", Data: jpeg},
		},
	}
	buf := EncodeRunRequest(p)
	acm := parseFields(t, buf)
	arr := parseFields(t, acm[ACM_RunRequest][0].data)
	ca := parseFields(t, arr[ARR_Action][0].data)
	uma := parseFields(t, ca[CA_UserMessageAction][0].data)
	um := parseFields(t, uma[UMA_UserMessage][0].data)
	scRaw, ok := um[UM_SelectedContext]
	if !ok {
		t.Fatal("UserMessage.selected_context (field 3) missing when images are present")
	}
	sc := parseFields(t, scRaw[0].data)
	entries := sc[SC_SelectedImages]
	if len(entries) != 2 {
		t.Fatalf("selected_images entries = %d, want 2", len(entries))
	}
	for i, want := range []struct {
		mime string
		data []byte
	}{{"image/png", png}, {"image/jpeg", jpeg}} {
		si := parseFields(t, entries[i].data)
		if got := string(si[SI_MimeType][0].data); got != want.mime {
			t.Errorf("image %d mime_type = %q, want %q", i, got, want.mime)
		}
		if !bytes.Equal(si[SI_Data][0].data, want.data) {
			t.Errorf("image %d data mismatch", i)
		}
		if len(si[SI_Uuid][0].data) == 0 {
			t.Errorf("image %d uuid empty", i)
		}
	}
}

// --- ModelDetails / RequestedModel display + max_mode fields ---

func TestEncodeRunRequestModelDetailsDisplayAndMaxMode(t *testing.T) {
	p := &RunRequestParams{
		ModelId:        "composer-2.5",
		DisplayModelId: "composer-2.5-display",
		DisplayName:    "Composer 2.5",
		MaxMode:        true,
		UserText:       "hi",
	}
	buf := EncodeRunRequest(p)
	arr := parseFields(t, parseFields(t, buf)[ACM_RunRequest][0].data)

	md := parseFields(t, arr[ARR_ModelDetails][0].data)
	if got := string(md[MD_DisplayModelId][0].data); got != "composer-2.5-display" {
		t.Fatalf("model_details.display_model_id = %q, want composer-2.5-display", got)
	}
	if got := string(md[MD_DisplayName][0].data); got != "Composer 2.5" {
		t.Fatalf("model_details.display_name = %q, want Composer 2.5", got)
	}
	maxModeField, ok := md[MD_MaxMode]
	if !ok || !maxModeField[0].isVar || maxModeField[0].varint != 1 {
		t.Fatalf("model_details.max_mode (field 7) = %+v, want varint 1", maxModeField)
	}

	rm := parseFields(t, arr[ARR_RequestedModel][0].data)
	rmMaxMode, ok := rm[RM_MaxMode]
	if !ok || !rmMaxMode[0].isVar || rmMaxMode[0].varint != 1 {
		t.Fatalf("requested_model.max_mode (field 2) = %+v, want varint 1", rmMaxMode)
	}
}

// --- Typed exec result encoders ---

func TestEncodeExecPiWriteRejectedRoundTrip(t *testing.T) {
	buf := EncodeExecPiWriteRejected(41, "exec-9", "denied by policy")

	acm := parseFields(t, buf)
	ecmRaw, ok := acm[ACM_ExecClientMessage]
	if !ok {
		t.Fatalf("AgentClientMessage.exec_client_message (field %d) missing", ACM_ExecClientMessage)
	}
	ecm := parseFields(t, ecmRaw[0].data)
	if got := ecm[ECM_Id][0].varint; got != 41 {
		t.Fatalf("ExecClientMessage.id = %d, want 41", got)
	}
	if got := string(ecm[ECM_ExecId][0].data); got != "exec-9" {
		t.Fatalf("ExecClientMessage.exec_id = %q, want exec-9", got)
	}
	// The rejection must land under the pi_write_result oneof case (49), not
	// under the request's own case number (48).
	resultRaw, ok := ecm[ECM_PiWriteResult]
	if !ok {
		t.Fatalf("ExecClientMessage.pi_write_result (case %d) missing, fields: %v", ECM_PiWriteResult, ecm)
	}
	result := parseFields(t, resultRaw[0].data)
	rejRaw, ok := result[PI_Rejected]
	if !ok {
		t.Fatalf("PiWriteExecResult.rejected (case %d) missing, fields: %v", PI_Rejected, result)
	}
	rej := parseFields(t, rejRaw[0].data)
	if got := string(rej[PIR_Reason][0].data); got != "denied by policy" {
		t.Fatalf("PiWriteExecRejected.reason = %q, want denied by policy", got)
	}
}

func TestEncodeExecReadRejectedUsesReadResultCase(t *testing.T) {
	buf := EncodeExecReadRejected(7, "exec-1", "/etc/passwd", "reads are not permitted")
	ecm := parseFields(t, parseFields(t, buf)[ACM_ExecClientMessage][0].data)
	resultRaw, ok := ecm[ECM_ReadResult]
	if !ok {
		t.Fatalf("ExecClientMessage.read_result (case %d) missing", ECM_ReadResult)
	}
	result := parseFields(t, resultRaw[0].data)
	rej := parseFields(t, result[RR_Rejected][0].data)
	if got := string(rej[REJ_Path][0].data); got != "/etc/passwd" {
		t.Fatalf("ReadRejected.path = %q", got)
	}
	if got := string(rej[REJ_Reason][0].data); got != "reads are not permitted" {
		t.Fatalf("ReadRejected.reason = %q", got)
	}
}

func TestEncodeExecMiniSweAgentBashRejectedUsesShellResultShape(t *testing.T) {
	buf := EncodeExecMiniSweAgentBashRejected(9, "exec-2", "rm -rf /", "/repo", "not allowlisted")
	ecm := parseFields(t, parseFields(t, buf)[ACM_ExecClientMessage][0].data)
	resultRaw, ok := ecm[ECM_MiniSweAgentBashResult]
	if !ok {
		t.Fatalf("ExecClientMessage.mini_swe_agent_bash_result (case %d) missing", ECM_MiniSweAgentBashResult)
	}
	result := parseFields(t, resultRaw[0].data)
	rej := parseFields(t, result[SR_Rejected][0].data)
	if got := string(rej[SREJ_Command][0].data); got != "rm -rf /" {
		t.Fatalf("ShellRejected.command = %q", got)
	}
	if got := string(rej[SREJ_Reason][0].data); got != "not allowlisted" {
		t.Fatalf("ShellRejected.reason = %q", got)
	}
}

func TestEncodeExecListMcpResourcesEmptyResultIsExplicitSuccess(t *testing.T) {
	buf := EncodeExecListMcpResourcesEmptyResult(3, "exec-3")
	ecm := parseFields(t, parseFields(t, buf)[ACM_ExecClientMessage][0].data)
	resultRaw, ok := ecm[ECM_ListMcpResourcesResult]
	if !ok {
		t.Fatalf("ExecClientMessage.list_mcp_resources_exec_result (case %d) missing", ECM_ListMcpResourcesResult)
	}
	result := parseFields(t, resultRaw[0].data)
	success, ok := result[LMR_Success]
	if !ok {
		t.Fatalf("ListMcpResourcesExecResult.success (case %d) missing", LMR_Success)
	}
	if len(success[0].data) != 0 {
		t.Fatalf("ListMcpResourcesSuccess must be empty, got %x", success[0].data)
	}
}

func TestEncodeExecMcpStateResultGroupsToolsUnderOneServer(t *testing.T) {
	buf := EncodeExecMcpStateResult(4, "exec-4", []McpToolDef{{
		Name:        "get_weather",
		Description: "Get weather",
	}})
	ecm := parseFields(t, parseFields(t, buf)[ACM_ExecClientMessage][0].data)
	result := parseFields(t, ecm[ECM_McpStateResult][0].data)
	success := parseFields(t, result[MSR_Success][0].data)
	servers := success[MSS_Servers]
	if len(servers) != 1 {
		t.Fatalf("McpStateSuccess.servers count = %d, want 1", len(servers))
	}
	server := parseFields(t, servers[0].data)
	if got := string(server[MSTS_ServerIdentifier][0].data); got != "proxy" {
		t.Fatalf("McpStateServer.server_identifier = %q, want proxy", got)
	}
	tools := server[MSTS_Tools]
	if len(tools) != 1 {
		t.Fatalf("McpStateServer.tools count = %d, want 1", len(tools))
	}
	tool := parseFields(t, tools[0].data)
	if got := string(tool[MTD_Name][0].data); got != "get_weather" {
		t.Fatalf("tool name = %q, want get_weather", got)
	}
}

func TestEncodeExecAllowlistPrecheckDefaults(t *testing.T) {
	// allowlisted=false is the proto3 default: the result message is empty
	// on the wire but the oneof case must still be present.
	buf := EncodeExecShellAllowlistPrecheckResult(5, "exec-5", false)
	ecm := parseFields(t, parseFields(t, buf)[ACM_ExecClientMessage][0].data)
	result, ok := ecm[ECM_ShellAllowlistPrecheckResult]
	if !ok {
		t.Fatalf("ExecClientMessage.shell_allowlist_precheck_result (case %d) missing", ECM_ShellAllowlistPrecheckResult)
	}
	if len(result[0].data) != 0 {
		t.Fatalf("allowlisted=false must serialize to an empty result, got %x", result[0].data)
	}

	buf = EncodeExecShellAllowlistPrecheckResult(5, "exec-5", true)
	ecm = parseFields(t, parseFields(t, buf)[ACM_ExecClientMessage][0].data)
	result = ecm[ECM_ShellAllowlistPrecheckResult]
	fields := parseFields(t, result[0].data)
	if got := fields[ALP_Allowlisted][0].varint; got != 1 {
		t.Fatalf("allowlisted = %d, want 1", got)
	}
}

// --- ExecClientControlMessage encoders (AgentClientMessage case 5) ---

func TestEncodeExecControlThrowWireShape(t *testing.T) {
	buf := EncodeExecControlThrow(12, "git diff unsupported", "NOT_IMPLEMENTED")
	acm := parseFields(t, buf)
	ctrlRaw, ok := acm[ACM_ExecClientControlMsg]
	if !ok {
		t.Fatalf("AgentClientMessage.exec_client_control_message (field %d) missing", ACM_ExecClientControlMsg)
	}
	ctrl := parseFields(t, ctrlRaw[0].data)
	throwRaw, ok := ctrl[ECCM_Throw]
	if !ok {
		t.Fatalf("ExecClientControlMessage.throw (case %d) missing", ECCM_Throw)
	}
	throw := parseFields(t, throwRaw[0].data)
	if got := throw[ECT_Id][0].varint; got != 12 {
		t.Fatalf("ExecClientThrow.id = %d, want 12", got)
	}
	if got := string(throw[ECT_Error][0].data); got != "git diff unsupported" {
		t.Fatalf("ExecClientThrow.error = %q", got)
	}
	if got := string(throw[ECT_ErrorCode][0].data); got != "NOT_IMPLEMENTED" {
		t.Fatalf("ExecClientThrow.error_code = %q", got)
	}
}

func TestEncodeExecControlStreamCloseWireShape(t *testing.T) {
	buf := EncodeExecControlStreamClose(12)
	ctrl := parseFields(t, parseFields(t, buf)[ACM_ExecClientControlMsg][0].data)
	sc := parseFields(t, ctrl[ECCM_StreamClose][0].data)
	if got := sc[ECSC_Id][0].varint; got != 12 {
		t.Fatalf("ExecClientStreamClose.id = %d, want 12", got)
	}
}

func TestEncodeExecControlHeartbeatWireShape(t *testing.T) {
	buf := EncodeExecControlHeartbeat(12)
	ctrl := parseFields(t, parseFields(t, buf)[ACM_ExecClientControlMsg][0].data)
	hb := parseFields(t, ctrl[ECCM_Heartbeat][0].data)
	if got := hb[ECHB_Id][0].varint; got != 12 {
		t.Fatalf("ExecClientHeartbeat.id = %d, want 12", got)
	}
}

// --- InteractionUpdate decoders (hand-built wire bytes) ---

// buildASMInteractionUpdate wraps one InteractionUpdate case in an
// AgentServerMessage, mirroring the server framing.
func buildASMInteractionUpdate(caseNum protowire.Number, inner []byte) []byte {
	iu := pwBytes(nil, caseNum, inner)
	return pwBytes(nil, ASM_InteractionUpdate, iu)
}

func TestDecodeTokenDeltaUpdate(t *testing.T) {
	tkdu := pwVarint(nil, TKDU_Tokens, 42)
	msg, err := DecodeAgentServerMessage(buildASMInteractionUpdate(IU_TokenDelta, tkdu))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != ServerMsgTokenDelta {
		t.Fatalf("Type = %v, want ServerMsgTokenDelta", msg.Type)
	}
	if msg.TokenDelta != 42 {
		t.Fatalf("TokenDelta = %d, want 42", msg.TokenDelta)
	}
}

func TestDecodeToolCallStartedUpdate(t *testing.T) {
	var tc []byte
	tc = pwBytes(tc, TC_McpToolCall, pwBytes(nil, MTC_Args, nil))
	tc = pwStr(tc, TC_ToolCallId, "tc-77")
	upd := pwStr(nil, TCU_CallId, "call-1")
	upd = pwBytes(upd, TCU_ToolCall, tc)

	msg, err := DecodeAgentServerMessage(buildASMInteractionUpdate(IU_ToolCallStarted, upd))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != ServerMsgToolCallStarted {
		t.Fatalf("Type = %v, want ServerMsgToolCallStarted", msg.Type)
	}
	if msg.CallId != "call-1" {
		t.Fatalf("CallId = %q, want call-1", msg.CallId)
	}
	if msg.ToolCallId != "tc-77" {
		t.Fatalf("ToolCallId = %q, want tc-77", msg.ToolCallId)
	}
	if msg.ToolCallCase != TC_McpToolCall {
		t.Fatalf("ToolCallCase = %d, want %d", msg.ToolCallCase, TC_McpToolCall)
	}
}

func TestDecodeToolCallCompletedUpdate(t *testing.T) {
	tc := pwStr(nil, TC_ToolCallId, "tc-78")
	upd := pwStr(nil, TCU_CallId, "call-2")
	upd = pwBytes(upd, TCU_ToolCall, tc)

	msg, err := DecodeAgentServerMessage(buildASMInteractionUpdate(IU_ToolCallCompleted, upd))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != ServerMsgToolCallCompleted {
		t.Fatalf("Type = %v, want ServerMsgToolCallCompleted", msg.Type)
	}
	if msg.CallId != "call-2" {
		t.Fatalf("CallId = %q, want call-2", msg.CallId)
	}
	if msg.ToolCallId != "tc-78" {
		t.Fatalf("ToolCallId = %q, want tc-78", msg.ToolCallId)
	}
}

func TestDecodePartialToolCallUpdate(t *testing.T) {
	var tc []byte
	tc = pwBytes(tc, TC_McpToolCall, pwBytes(nil, MTC_Args, nil))
	upd := pwStr(nil, PTCU_CallId, "call-3")
	upd = pwBytes(upd, PTCU_ToolCall, tc)
	upd = pwStr(upd, PTCU_ArgsTextDelta, `{"city":"Seo`)

	msg, err := DecodeAgentServerMessage(buildASMInteractionUpdate(IU_PartialToolCall, upd))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != ServerMsgPartialToolCall {
		t.Fatalf("Type = %v, want ServerMsgPartialToolCall", msg.Type)
	}
	if msg.CallId != "call-3" {
		t.Fatalf("CallId = %q, want call-3", msg.CallId)
	}
	if msg.ArgsTextDelta != `{"city":"Seo` {
		t.Fatalf("ArgsTextDelta = %q", msg.ArgsTextDelta)
	}
	if msg.ToolCallCase != TC_McpToolCall {
		t.Fatalf("ToolCallCase = %d, want %d", msg.ToolCallCase, TC_McpToolCall)
	}
}

func TestDecodeToolCallDeltaUpdate(t *testing.T) {
	delta := pwBytes(nil, 1, []byte{0x0a, 0x00}) // opaque shell delta variant
	upd := pwStr(nil, TCDU_CallId, "call-4")
	upd = pwBytes(upd, TCDU_ToolCallDelta, delta)

	msg, err := DecodeAgentServerMessage(buildASMInteractionUpdate(IU_ToolCallDelta, upd))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != ServerMsgToolCallDelta {
		t.Fatalf("Type = %v, want ServerMsgToolCallDelta", msg.Type)
	}
	if msg.CallId != "call-4" {
		t.Fatalf("CallId = %q, want call-4", msg.CallId)
	}
	if !bytes.Equal(msg.ToolCallDeltaData, delta) {
		t.Fatalf("ToolCallDeltaData = %x, want %x", msg.ToolCallDeltaData, delta)
	}
}

func TestDecodeTurnEndedUpdate(t *testing.T) {
	msg, err := DecodeAgentServerMessage(buildASMInteractionUpdate(IU_TurnEnded, nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != ServerMsgTurnEnded {
		t.Fatalf("Type = %v, want ServerMsgTurnEnded", msg.Type)
	}
}

// TestDecodeConversationCheckpointCarriesUsedTokens pins senpi's
// applyCheckpointTokenDetails contract (cursor-agent.ts:3492): the mid-turn
// ConversationStateStructure checkpoint carries the server's live conversation
// size at token_details.used_tokens (CSS field 5 -> CTD field 1), and decoding a
// checkpoint must surface it. Fails while the decoder captures only the raw
// checkpoint bytes, which leaves the live-usage signal at zero.
//
// Given: an AgentServerMessage carrying a conversation checkpoint whose
// token_details.used_tokens is 148256 (senpi's cursor-usage.test.ts figure).
// When:  the frame is decoded.
// Then:  the decoded message reports that used-tokens value.
func TestDecodeConversationCheckpointCarriesUsedTokens(t *testing.T) {
	const usedTokens = 148256
	tokenDetails := pwVarint(nil, CTD_UsedTokens, usedTokens)
	tokenDetails = pwVarint(tokenDetails, CTD_MaxTokens, 200000)
	checkpoint := pwBytes(nil, CSS_RootPromptMessagesJson, []byte(`{"role":"system"}`))
	checkpoint = pwBytes(checkpoint, CSS_TokenDetails, tokenDetails)
	frame := pwBytes(nil, ASM_ConversationCheckpoint, checkpoint)

	msg, err := DecodeAgentServerMessage(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != ServerMsgCheckpoint {
		t.Fatalf("Type = %v, want ServerMsgCheckpoint", msg.Type)
	}
	if msg.CheckpointUsedTokens != usedTokens {
		t.Errorf("CheckpointUsedTokens = %d, want %d", msg.CheckpointUsedTokens, usedTokens)
	}
	if !bytes.Equal(msg.CheckpointData, checkpoint) {
		t.Errorf("CheckpointData = %x, want the raw checkpoint %x", msg.CheckpointData, checkpoint)
	}
}

// TestDecodeConversationCheckpointWithoutTokenDetails covers the fresh-conversation
// shape: senpi reads `checkpoint.tokenDetails?.usedTokens` optionally, so a
// checkpoint with no token_details must decode to zero rather than a stale value.
func TestDecodeConversationCheckpointWithoutTokenDetails(t *testing.T) {
	checkpoint := pwBytes(nil, CSS_RootPromptMessagesJson, []byte(`{"role":"system"}`))

	msg, err := DecodeAgentServerMessage(pwBytes(nil, ASM_ConversationCheckpoint, checkpoint))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.CheckpointUsedTokens != 0 {
		t.Errorf("CheckpointUsedTokens = %d, want 0", msg.CheckpointUsedTokens)
	}
}

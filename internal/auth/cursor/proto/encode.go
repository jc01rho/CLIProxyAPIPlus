// Package proto provides protobuf encoding for Cursor's gRPC API,
// using dynamicpb with the embedded FileDescriptorProto from agent.proto.
// This mirrors the cursor-auth TS plugin's use of @bufbuild/protobuf create()+toBinary().
package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// --- Public types ---

// RunRequestParams holds all data needed to build an AgentRunRequest.
type RunRequestParams struct {
	ModelId         string
	ReasoningEffort string
	SystemPrompt    string
	UserText        string
	MessageId       string
	ConversationId  string
	Images          []ImageData
	Turns           []TurnData
	McpTools        []McpToolDef
	BlobStore       map[string][]byte // hex(sha256) -> data, populated during encoding
	RawCheckpoint   []byte            // if non-nil, use as conversation_state directly (from server checkpoint)
}

type ImageData struct {
	MimeType string
	Data     []byte
}

type TurnData struct {
	UserText      string
	AssistantText string
}

type McpToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ModelParameter is a single RequestedModel parameter (id/value pair).
type ModelParameter struct {
	ID    string
	Value string
}

// --- Helper: create a dynamic message and set fields ---

func newMsg(name string) *dynamicpb.Message {
	return dynamicpb.NewMessage(Msg(name))
}

func field(msg *dynamicpb.Message, name string) protoreflect.FieldDescriptor {
	return msg.Descriptor().Fields().ByName(protoreflect.Name(name))
}

func setStr(msg *dynamicpb.Message, name, val string) {
	if val != "" {
		msg.Set(field(msg, name), protoreflect.ValueOfString(val))
	}
}

func setBytes(msg *dynamicpb.Message, name string, val []byte) {
	if len(val) > 0 {
		msg.Set(field(msg, name), protoreflect.ValueOfBytes(val))
	}
}

func setUint32(msg *dynamicpb.Message, name string, val uint32) {
	msg.Set(field(msg, name), protoreflect.ValueOfUint32(val))
}

func setBool(msg *dynamicpb.Message, name string, val bool) {
	msg.Set(field(msg, name), protoreflect.ValueOfBool(val))
}

func setMsg(msg *dynamicpb.Message, name string, sub *dynamicpb.Message) {
	msg.Set(field(msg, name), protoreflect.ValueOfMessage(sub.ProtoReflect()))
}

func marshal(msg *dynamicpb.Message) []byte {
	b, err := proto.Marshal(msg)
	if err != nil {
		panic("cursor proto marshal: " + err.Error())
	}
	return b
}

// --- protowire helpers ---

func pwVarint(buf []byte, num protowire.Number, v uint64) []byte {
	buf = protowire.AppendTag(buf, num, protowire.VarintType)
	return protowire.AppendVarint(buf, v)
}

func pwBytes(buf []byte, num protowire.Number, v []byte) []byte {
	buf = protowire.AppendTag(buf, num, protowire.BytesType)
	return protowire.AppendBytes(buf, v)
}

func pwStr(buf []byte, num protowire.Number, v string) []byte {
	buf = protowire.AppendTag(buf, num, protowire.BytesType)
	return protowire.AppendString(buf, v)
}

// --- Encode functions mirroring cursor-fetch.ts ---

// EncodeHeartbeat returns an encoded AgentClientMessage with clientHeartbeat.
// Mirrors: create(AgentClientMessageSchema, { message: { case: 'clientHeartbeat', value: create(ClientHeartbeatSchema, {}) } })
func EncodeHeartbeat() []byte {
	hb := newMsg("ClientHeartbeat")
	acm := newMsg("AgentClientMessage")
	setMsg(acm, "client_heartbeat", hb)
	return marshal(acm)
}

// EncodeRunRequest builds a full AgentClientMessage wrapping an AgentRunRequest.
//
// Wire shape follows cursor-agent CLI traffic (cross-checked against the
// OmniRoute, 9router, and opencodex reverse-engineered clients):
//   - UserMessage carries message_id, a selected_context envelope (empty when
//     no images) and mode=1 — without these placeholders the server may accept
//     the request but never stream a response;
//   - the mcp_tools envelope is always present (empty when no tools) —
//     omitting it entirely makes cursor error;
//   - model_details and requested_model are mutually exclusive on the wire
//     (opencodex request-builder.ts compose_requested_model): model_details
//     carries the plain resolved id when there are no explicit model
//     parameters, requested_model (with its id/value parameter list) is sent
//     only when there are explicit parameters (e.g. grok-*-fast effort+fast).
//     Sending both together — even with requested_model carrying only a bare
//     model_id and no parameters — makes the backend see conflicting model
//     selections and answer with Connect not_found;
//   - conversation_id (field 5) is the only identifier sent; the real
//     AgentRunRequest schema (agent.v1.AgentRunRequest, msg 91 in
//     opencodex's generated agent_pb.ts) defines only fields 1-9 -- earlier
//     reverse-engineered field 12 (varint placeholder) and field 16
//     (duplicate request_id string) do not exist and must not be sent.
//
// If p.RawCheckpoint is set, it is used directly as the conversation_state
// bytes (from a previous conversation_checkpoint_update), skipping manual
// turn construction.
func EncodeRunRequest(p *RunRequestParams) []byte {
	if p.BlobStore == nil {
		p.BlobStore = make(map[string][]byte)
	}

	var cssBytes []byte
	if p.RawCheckpoint != nil {
		cssBytes = p.RawCheckpoint
	} else {
		cssBytes = buildConversationStateBytes(p)
	}

	// ConversationAction → UserMessageAction → UserMessage
	umBytes := buildUserMessageBytes(p.UserText, p.MessageId, p.Images)
	caBytes := pwBytes(nil, CA_UserMessageAction, pwBytes(nil, UMA_UserMessage, umBytes))

	// model_details XOR requested_model — see EncodeRunRequest doc comment.
	resolvedID, rmParams := ResolveRequestedModel(p.ModelId, p.ReasoningEffort)
	mdBytes := buildModelDetailsBytes(resolvedID)
	var rmBytes []byte
	if len(rmParams) > 0 {
		rmBytes = buildRequestedModelBytes(resolvedID, rmParams)
	}

	// McpTools envelope — always present, even when empty.
	mcpBytes := buildMcpToolsBytes(p.McpTools)

	conversationID := p.ConversationId
	if conversationID == "" {
		conversationID = generateId()
	}

	arrBuf := pwBytes(nil, ARR_ConversationState, cssBytes)
	arrBuf = pwBytes(arrBuf, ARR_Action, caBytes)
	if len(rmParams) == 0 {
		arrBuf = pwBytes(arrBuf, ARR_ModelDetails, mdBytes)
	}
	arrBuf = pwBytes(arrBuf, ARR_McpTools, mcpBytes)
	arrBuf = pwStr(arrBuf, ARR_ConversationId, conversationID)
	if len(rmParams) > 0 {
		arrBuf = pwBytes(arrBuf, ARR_RequestedModel, rmBytes)
	}

	acmBuf := pwBytes(nil, ACM_RunRequest, arrBuf)

	if p.RawCheckpoint != nil {
		log.Debugf("cursor encode: built RunRequest with checkpoint (%d bytes), total=%d bytes", len(p.RawCheckpoint), len(acmBuf))
	}
	return acmBuf
}

// buildUserMessageBytes encodes a UserMessage with cursor-agent-compliant
// placeholders: message_id fallback, an always-present selected_context
// envelope (empty when no images) and mode=1.
func buildUserMessageBytes(text, messageID string, images []ImageData) []byte {
	var buf []byte
	buf = pwStr(buf, UM_Text, text)
	if messageID == "" {
		messageID = generateId()
	}
	buf = pwStr(buf, UM_MessageId, messageID)

	var scBytes []byte
	if len(images) > 0 {
		sc := newMsg("SelectedContext")
		imgsField := field(sc, "selected_images")
		imgsList := sc.Mutable(imgsField).List()
		for _, img := range images {
			si := newMsg("SelectedImage")
			setStr(si, "uuid", generateId())
			setStr(si, "mime_type", img.MimeType)
			setBytes(si, "data", img.Data)
			imgsList.Append(protoreflect.ValueOfMessage(si.ProtoReflect()))
		}
		scBytes = marshal(sc)
	}
	buf = pwBytes(buf, UM_SelectedContext, scBytes)
	buf = pwVarint(buf, UM_Mode, 1)
	return buf
}

// buildConversationStateBytes encodes root prompt blob + conversation turns.
func buildConversationStateBytes(p *RunRequestParams) []byte {
	// --- Conversation turns ---
	var turnBytes [][]byte
	for _, turn := range p.Turns {
		umBytes := buildUserMessageBytes(turn.UserText, "", nil)

		var stepBytes [][]byte
		if turn.AssistantText != "" {
			am := newMsg("AssistantMessage")
			setStr(am, "text", turn.AssistantText)
			step := newMsg("ConversationStep")
			setMsg(step, "assistant_message", am)
			stepBytes = append(stepBytes, marshal(step))
		}

		agentTurn := newMsg("AgentConversationTurnStructure")
		setBytes(agentTurn, "user_message", umBytes)
		for _, sb := range stepBytes {
			stepsField := field(agentTurn, "steps")
			list := agentTurn.Mutable(stepsField).List()
			list.Append(protoreflect.ValueOfBytes(sb))
		}

		cts := newMsg("ConversationTurnStructure")
		setMsg(cts, "agent_conversation_turn", agentTurn)
		turnBytes = append(turnBytes, marshal(cts))
	}

	// --- System prompt blob ---
	systemJSON, _ := json.Marshal(map[string]string{"role": "system", "content": p.SystemPrompt})
	blobId := sha256Sum(systemJSON)
	p.BlobStore[hex.EncodeToString(blobId)] = systemJSON

	// --- ConversationStateStructure ---
	css := newMsg("ConversationStateStructure")
	rootField := field(css, "root_prompt_messages_json")
	rootList := css.Mutable(rootField).List()
	rootList.Append(protoreflect.ValueOfBytes(blobId))
	turnsField := field(css, "turns")
	turnsList := css.Mutable(turnsField).List()
	for _, tb := range turnBytes {
		turnsList.Append(protoreflect.ValueOfBytes(tb))
	}
	turnsOldField := field(css, "turns_old")
	if turnsOldField != nil {
		turnsOldList := css.Mutable(turnsOldField).List()
		for _, tb := range turnBytes {
			turnsOldList.Append(protoreflect.ValueOfBytes(tb))
		}
	}
	return marshal(css)
}

// buildModelDetailsBytes encodes ModelDetails with the resolved wire model id.
func buildModelDetailsBytes(modelID string) []byte {
	md := newMsg("ModelDetails")
	setStr(md, "model_id", modelID)
	setStr(md, "display_model_id", modelID)
	setStr(md, "display_name", modelID)
	return marshal(md)
}

// buildMcpToolsBytes encodes the McpTools envelope. It is always present on
// the wire — an empty message when no tools are declared — because cursor
// errors when the envelope is omitted entirely.
func buildMcpToolsBytes(tools []McpToolDef) []byte {
	mcpTools := newMsg("McpTools")
	if len(tools) > 0 {
		toolsField := field(mcpTools, "mcp_tools")
		toolsList := mcpTools.Mutable(toolsField).List()
		for _, tool := range tools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
	}
	return marshal(mcpTools)
}

// buildRequestedModelBytes encodes a RequestedModel message.
func buildRequestedModelBytes(modelID string, params []ModelParameter) []byte {
	var buf []byte
	buf = pwStr(buf, RM_ModelId, modelID)
	for _, p := range params {
		var mp []byte
		mp = pwStr(mp, MP_Id, p.ID)
		mp = pwStr(mp, MP_Value, p.Value)
		buf = pwBytes(buf, RM_Parameters, mp)
	}
	return buf
}

// ResumeRequestParams holds data for a ResumeAction request.
type ResumeRequestParams struct {
	ModelId        string
	ConversationId string
	McpTools       []McpToolDef
}

// EncodeResumeRequest builds an AgentClientMessage with ResumeAction.
// Used to resume a conversation by conversation_id without re-sending full history.
func EncodeResumeRequest(p *ResumeRequestParams) []byte {
	// RequestContext with tools
	rc := newMsg("RequestContext")
	if len(p.McpTools) > 0 {
		toolsField := field(rc, "tools")
		toolsList := rc.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
	}

	// ResumeAction
	ra := newMsg("ResumeAction")
	setMsg(ra, "request_context", rc)

	// ConversationAction with resume_action
	ca := newMsg("ConversationAction")
	setMsg(ca, "resume_action", ra)

	// ModelDetails (resolved wire id)
	resolvedID, _ := ResolveRequestedModel(p.ModelId, "")
	md := newMsg("ModelDetails")
	setStr(md, "model_id", resolvedID)
	setStr(md, "display_model_id", resolvedID)
	setStr(md, "display_name", resolvedID)

	// AgentRunRequest — no conversation_state needed for resume
	arr := newMsg("AgentRunRequest")
	setMsg(arr, "action", ca)
	setMsg(arr, "model_details", md)
	setStr(arr, "conversation_id", p.ConversationId)

	// McpTools at top level
	if len(p.McpTools) > 0 {
		mcpTools := newMsg("McpTools")
		toolsField := field(mcpTools, "mcp_tools")
		toolsList := mcpTools.Mutable(toolsField).List()
		for _, tool := range p.McpTools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
		setMsg(arr, "mcp_tools", mcpTools)
	}

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "run_request", arr)
	return marshal(acm)
}

// --- KV response encoders ---
// Mirrors handleKvMessage() in cursor-fetch.ts

// EncodeKvGetBlobResult responds to a getBlobArgs request.
func EncodeKvGetBlobResult(kvId uint32, blobData []byte) []byte {
	result := newMsg("GetBlobResult")
	if blobData != nil {
		setBytes(result, "blob_data", blobData)
	}

	kvc := newMsg("KvClientMessage")
	setUint32(kvc, "id", kvId)
	setMsg(kvc, "get_blob_result", result)

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "kv_client_message", kvc)
	return marshal(acm)
}

// EncodeKvSetBlobResult responds to a setBlobArgs request.
func EncodeKvSetBlobResult(kvId uint32) []byte {
	result := newMsg("SetBlobResult")

	kvc := newMsg("KvClientMessage")
	setUint32(kvc, "id", kvId)
	setMsg(kvc, "set_blob_result", result)

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "kv_client_message", kvc)
	return marshal(acm)
}

// --- Exec response encoders ---
// Mirrors handleExecMessage() and sendExec() in cursor-fetch.ts

// EncodeExecRequestContextResult responds to requestContextArgs with tool definitions.
func EncodeExecRequestContextResult(execMsgId uint32, execId string, tools []McpToolDef) []byte {
	// RequestContext with tools
	rc := newMsg("RequestContext")
	if len(tools) > 0 {
		toolsField := field(rc, "tools")
		toolsList := rc.Mutable(toolsField).List()
		for _, tool := range tools {
			td := newMsg("McpToolDefinition")
			setStr(td, "name", tool.Name)
			setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			setStr(td, "provider_identifier", "proxy")
			setStr(td, "tool_name", tool.Name)
			toolsList.Append(protoreflect.ValueOfMessage(td.ProtoReflect()))
		}
	}

	// RequestContextSuccess
	rcs := newMsg("RequestContextSuccess")
	setMsg(rcs, "request_context", rc)

	// RequestContextResult (oneof success)
	rcr := newMsg("RequestContextResult")
	setMsg(rcr, "success", rcs)

	return encodeExecClientMsg(execMsgId, execId, "request_context_result", rcr)
}

// EncodeExecMcpResult responds with MCP tool result.
func EncodeExecMcpResult(execMsgId uint32, execId string, content string, isError bool) []byte {
	textContent := newMsg("McpTextContent")
	setStr(textContent, "text", content)

	contentItem := newMsg("McpToolResultContentItem")
	setMsg(contentItem, "text", textContent)

	success := newMsg("McpSuccess")
	contentField := field(success, "content")
	contentList := success.Mutable(contentField).List()
	contentList.Append(protoreflect.ValueOfMessage(contentItem.ProtoReflect()))
	setBool(success, "is_error", isError)

	result := newMsg("McpResult")
	setMsg(result, "success", success)

	return encodeExecClientMsg(execMsgId, execId, "mcp_result", result)
}

// EncodeExecMcpError responds with MCP error.
func EncodeExecMcpError(execMsgId uint32, execId string, errMsg string) []byte {
	mcpErr := newMsg("McpError")
	setStr(mcpErr, "error", errMsg)

	result := newMsg("McpResult")
	setMsg(result, "error", mcpErr)

	return encodeExecClientMsg(execMsgId, execId, "mcp_result", result)
}

// --- Rejection encoders (mirror handleExecMessage rejections) ---

func EncodeExecReadRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("ReadRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("ReadResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "read_result", result)
}

func EncodeExecShellRejected(execMsgId uint32, execId string, command, workDir, reason string) []byte {
	rej := newMsg("ShellRejected")
	setStr(rej, "command", command)
	setStr(rej, "working_directory", workDir)
	setStr(rej, "reason", reason)
	result := newMsg("ShellResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "shell_result", result)
}

func EncodeExecWriteRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("WriteRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("WriteResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "write_result", result)
}

func EncodeExecDeleteRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("DeleteRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("DeleteResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "delete_result", result)
}

func EncodeExecLsRejected(execMsgId uint32, execId string, path, reason string) []byte {
	rej := newMsg("LsRejected")
	setStr(rej, "path", path)
	setStr(rej, "reason", reason)
	result := newMsg("LsResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "ls_result", result)
}

func EncodeExecGrepError(execMsgId uint32, execId string, errMsg string) []byte {
	grepErr := newMsg("GrepError")
	setStr(grepErr, "error", errMsg)
	result := newMsg("GrepResult")
	setMsg(result, "error", grepErr)
	return encodeExecClientMsg(execMsgId, execId, "grep_result", result)
}

func EncodeExecFetchError(execMsgId uint32, execId string, url, errMsg string) []byte {
	fetchErr := newMsg("FetchError")
	setStr(fetchErr, "url", url)
	setStr(fetchErr, "error", errMsg)
	result := newMsg("FetchResult")
	setMsg(result, "error", fetchErr)
	return encodeExecClientMsg(execMsgId, execId, "fetch_result", result)
}

func EncodeExecDiagnosticsResult(execMsgId uint32, execId string) []byte {
	result := newMsg("DiagnosticsResult")
	return encodeExecClientMsg(execMsgId, execId, "diagnostics_result", result)
}

func EncodeExecBackgroundShellSpawnRejected(execMsgId uint32, execId string, command, workDir, reason string) []byte {
	rej := newMsg("ShellRejected")
	setStr(rej, "command", command)
	setStr(rej, "working_directory", workDir)
	setStr(rej, "reason", reason)
	result := newMsg("BackgroundShellSpawnResult")
	setMsg(result, "rejected", rej)
	return encodeExecClientMsg(execMsgId, execId, "background_shell_spawn_result", result)
}

func EncodeExecWriteShellStdinError(execMsgId uint32, execId string, errMsg string) []byte {
	wsErr := newMsg("WriteShellStdinError")
	setStr(wsErr, "error", errMsg)
	result := newMsg("WriteShellStdinResult")
	setMsg(result, "error", wsErr)
	return encodeExecClientMsg(execMsgId, execId, "write_shell_stdin_result", result)
}

// encodeExecClientMsg wraps an exec result in AgentClientMessage.
// Mirrors sendExec() in cursor-fetch.ts.
func encodeExecClientMsg(id uint32, execId string, resultFieldName string, resultMsg *dynamicpb.Message) []byte {
	ecm := newMsg("ExecClientMessage")
	setUint32(ecm, "id", id)
	// Force set exec_id even if empty - Cursor requires this field to be set
	ecm.Set(field(ecm, "exec_id"), protoreflect.ValueOfString(execId))

	// Debug: check if field exists
	fd := field(ecm, resultFieldName)
	if fd == nil {
		panic(fmt.Sprintf("field %q NOT FOUND in ExecClientMessage! Available fields: %v", resultFieldName, listFields(ecm)))
	}

	// Debug: log the actual field being set
	log.Debugf("encodeExecClientMsg: setting field %q (number=%d, kind=%s)", fd.Name(), fd.Number(), fd.Kind())

	ecm.Set(fd, protoreflect.ValueOfMessage(resultMsg.ProtoReflect()))

	acm := newMsg("AgentClientMessage")
	setMsg(acm, "exec_client_message", ecm)
	return marshal(acm)
}

func listFields(msg *dynamicpb.Message) []string {
	var names []string
	for i := 0; i < msg.Descriptor().Fields().Len(); i++ {
		names = append(names, string(msg.Descriptor().Fields().Get(i).Name()))
	}
	return names
}

// --- Utilities ---

// jsonToProtobufValueBytes converts a JSON schema (json.RawMessage) to protobuf Value binary.
// This mirrors the TS pattern: toBinary(ValueSchema, fromJson(ValueSchema, jsonSchema))
func jsonToProtobufValueBytes(jsonData json.RawMessage) []byte {
	if len(jsonData) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(jsonData, &v); err != nil {
		return jsonData // fallback to raw JSON if parsing fails
	}
	pbVal, err := structpb.NewValue(v)
	if err != nil {
		return jsonData // fallback
	}
	b, err := proto.Marshal(pbVal)
	if err != nil {
		return jsonData // fallback
	}
	return b
}

// ProtobufValueBytesToJSON converts protobuf Value binary back to JSON.
// This mirrors the TS pattern: toJson(ValueSchema, fromBinary(ValueSchema, value))
func ProtobufValueBytesToJSON(data []byte) (interface{}, error) {
	val := &structpb.Value{}
	if err := proto.Unmarshal(data, val); err != nil {
		return nil, err
	}
	return val.AsInterface(), nil
}

func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

var idCounter uint64

func generateId() string {
	idCounter++
	h := sha256.Sum256([]byte{byte(idCounter), byte(idCounter >> 8), byte(idCounter >> 16)})
	return hex.EncodeToString(h[:16])
}

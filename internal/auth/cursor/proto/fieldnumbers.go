// Package proto provides hand-rolled protobuf encode/decode for Cursor's gRPC API.
// Field numbers are extracted from the TypeScript generated proto/agent_pb.ts in alma-plugins/cursor-auth.
package proto

// AgentClientMessage (msg 118) oneof "message"
const (
	ACM_RunRequest           = 1 // AgentRunRequest
	ACM_ExecClientMessage    = 2 // ExecClientMessage
	ACM_KvClientMessage      = 3 // KvClientMessage
	ACM_ConversationAction   = 4 // ConversationAction
	ACM_ExecClientControlMsg = 5 // ExecClientControlMessage
	ACM_InteractionResponse  = 6 // InteractionResponse
	ACM_ClientHeartbeat      = 7 // ClientHeartbeat
)

// AgentServerMessage (msg 119) oneof "message"
const (
	ASM_InteractionUpdate        = 1 // InteractionUpdate
	ASM_ExecServerMessage        = 2 // ExecServerMessage
	ASM_ConversationCheckpoint   = 3 // ConversationStateStructure
	ASM_KvServerMessage          = 4 // KvServerMessage
	ASM_ExecServerControlMessage = 5 // ExecServerControlMessage
	ASM_InteractionQuery         = 7 // InteractionQuery
)

// AgentRunRequest (msg 91)
const (
	ARR_ConversationState  = 1 // ConversationStateStructure
	ARR_Action             = 2 // ConversationAction
	ARR_ModelDetails       = 3 // ModelDetails
	ARR_McpTools           = 4 // McpTools
	ARR_ConversationId     = 5 // string (optional)
	ARR_RequestedModel     = 9 // RequestedModel
	ARR_CustomSystemPrompt = 8 // string
)

// Fields 12 and 16 were previously written as reverse-engineered guesses
// ("UnknownVarint12" / "RequestId") but do not exist in the real
// agent.v1.AgentRunRequest schema (msg 91), which defines only fields 1-9
// (see opencodex's generated agent_pb.ts). Sending them added spurious
// unknown fields to the wire and has been removed.

// ConversationStateStructure (msg 83)
const (
	CSS_RootPromptMessagesJson = 1  // repeated bytes
	CSS_TurnsOld               = 2  // repeated bytes (deprecated)
	CSS_Todos                  = 3  // repeated bytes
	CSS_PendingToolCalls       = 4  // repeated string
	CSS_Turns                  = 8  // repeated bytes (CURRENT field for turns)
	CSS_PreviousWorkspaceUris  = 9  // repeated string
	CSS_SelfSummaryCount       = 17 // uint32
	CSS_ReadPaths              = 18 // repeated string
)

// ConversationAction (msg 54) oneof "action"
const (
	CA_UserMessageAction = 1 // UserMessageAction
	CA_ResumeAction      = 2 // ResumeAction
)

// UserMessageAction (msg 55)
const (
	UMA_UserMessage    = 1 // UserMessage
	UMA_RequestContext = 2 // RequestContext
)

// UserMessage (msg 63)
const (
	UM_Text            = 1 // string
	UM_MessageId       = 2 // string
	UM_SelectedContext = 3 // SelectedContext
	UM_Mode            = 4 // varint
)

// SelectedContext (note: selected_images is field 1 per agent.proto
// "SelectedContext"; the 3 in some notes refers to UserMessage.selected_context)
const (
	SC_SelectedImages = 1 // repeated SelectedImage
)

// SelectedImage
const (
	SI_BlobId   = 1 // bytes (oneof dataOrBlobId)
	SI_Uuid     = 2 // string
	SI_Path     = 3 // string
	SI_MimeType = 7 // string
	SI_Data     = 8 // bytes (oneof dataOrBlobId)
)

// ModelDetails (msg 88)
const (
	MD_ModelId          = 1 // string
	MD_ThinkingDetails  = 2 // ThinkingDetails (optional)
	MD_DisplayModelId   = 3 // string
	MD_DisplayName      = 4 // string
	MD_DisplayNameShort = 5 // string
	MD_MaxMode          = 7 // optional bool
)

// RequestedModel (real schema: max_mode=2, parameters=3).
const (
	RM_ModelId    = 1 // string
	RM_MaxMode    = 2 // bool (proto3 default false — never serialized)
	RM_Parameters = 3 // repeated ModelParameter
)

// ModelParameter
const (
	MP_Id    = 1 // string
	MP_Value = 2 // string
)

// RequestContext (additional field; RC_Rules/RC_Tools declared below)
const (
	RC_Env = 4 // RequestContextEnv
)

// RequestContextEnv
const (
	RCE_TimeZone = 10 // string (IANA name, e.g. "Asia/Seoul")
)

// McpTools (msg 307)
const (
	MT_McpTools = 1 // repeated McpToolDefinition
)

// McpToolDefinition (msg 306)
const (
	MTD_Name               = 1 // string
	MTD_Description        = 2 // string
	MTD_InputSchema        = 3 // bytes
	MTD_ProviderIdentifier = 4 // string
	MTD_ToolName           = 5 // string
)

// ConversationTurnStructure (msg 70) oneof "turn"
const (
	CTS_AgentConversationTurn = 1 // AgentConversationTurnStructure
)

// AgentConversationTurnStructure (msg 72)
const (
	ACTS_UserMessage = 1 // bytes (serialized UserMessage)
	ACTS_Steps       = 2 // repeated bytes (serialized ConversationStep)
)

// ConversationStep (msg 53) oneof "message"
const (
	CS_AssistantMessage = 1 // AssistantMessage
)

// AssistantMessage
const (
	AM_Text = 1 // string
)

// --- Server-side message fields ---

// InteractionUpdate oneof "message"
const (
	IU_TextDelta         = 1  // TextDeltaUpdate
	IU_ToolCallStarted   = 2  // ToolCallStartedUpdate
	IU_ToolCallCompleted = 3  // ToolCallCompletedUpdate
	IU_ThinkingDelta     = 4  // ThinkingDeltaUpdate
	IU_ThinkingCompleted = 5  // ThinkingCompletedUpdate
	IU_PartialToolCall   = 7  // PartialToolCallUpdate
	IU_TokenDelta        = 8  // TokenDeltaUpdate
	IU_Heartbeat         = 13 // HeartbeatUpdate (schema-only)
	IU_TurnEnded         = 14 // TurnEndedUpdate
	IU_ToolCallDelta     = 15 // ToolCallDeltaUpdate
	IU_StepStarted       = 16 // StepStartedUpdate
	IU_StepCompleted     = 17 // StepCompletedUpdate
)

// TurnEndedUpdate billed token fields (Cursor production schema 2026.08.11)
const (
	TEU_InputTokens      = 1 // int64
	TEU_OutputTokens     = 2 // int64
	TEU_CacheReadTokens  = 3 // int64
	TEU_CacheWriteTokens = 4 // int64
	TEU_ReasoningTokens  = 5 // int64
)

// ToolCallStartedUpdate / ToolCallCompletedUpdate (identical shapes)
const (
	TCU_CallId   = 1 // string
	TCU_ToolCall = 2 // ToolCall
)

// PartialToolCallUpdate
const (
	PTCU_CallId        = 1 // string
	PTCU_ToolCall      = 2 // ToolCall
	PTCU_ArgsTextDelta = 3 // string (aggregated args JSON text so far)
)

// ToolCallDeltaUpdate
const (
	TCDU_CallId        = 1 // string
	TCDU_ToolCallDelta = 2 // ToolCallDelta
)

// TokenDeltaUpdate
const (
	TKDU_Tokens = 1 // int32
)

// ToolCall envelope fields (the oneof variants are per-tool submessages;
// modern builds carry the call id on the envelope)
const (
	TC_McpToolCall = 15 // McpToolCall (oneof "tool")
	TC_ToolCallId  = 57 // optional string
)

// McpToolCall
const (
	MTC_Args = 1 // McpArgs
)

// TextDeltaUpdate (msg 92)
const (
	TDU_Text = 1 // string
)

// ThinkingDeltaUpdate (msg 97)
const (
	TKD_Text = 1 // string
)

// ConversationTokenDetails (ConversationStateStructure field 5)
const (
	CTD_UsedTokens = 1 // uint32
	CTD_MaxTokens  = 2 // uint32
)

// KvServerMessage (msg 271)
const (
	KSM_Id          = 1 // uint32
	KSM_GetBlobArgs = 2 // GetBlobArgs
	KSM_SetBlobArgs = 3 // SetBlobArgs
)

// GetBlobArgs (msg 267)
const (
	GBA_BlobId = 1 // bytes
)

// SetBlobArgs (msg 269)
const (
	SBA_BlobId   = 1 // bytes
	SBA_BlobData = 2 // bytes
)

// KvClientMessage (msg 272)
const (
	KCM_Id            = 1 // uint32
	KCM_GetBlobResult = 2 // GetBlobResult
	KCM_SetBlobResult = 3 // SetBlobResult
)

// GetBlobResult (msg 268)
const (
	GBR_BlobData = 1 // bytes (optional)
)

// ExecServerMessage
const (
	ESM_Id     = 1  // uint32
	ESM_ExecId = 15 // string
	// oneof message:
	ESM_ShellArgs            = 2  // ShellArgs
	ESM_WriteArgs            = 3  // WriteArgs
	ESM_DeleteArgs           = 4  // DeleteArgs
	ESM_GrepArgs             = 5  // GrepArgs
	ESM_ReadArgs             = 7  // ReadArgs (NOTE: 6 is skipped)
	ESM_LsArgs               = 8  // LsArgs
	ESM_DiagnosticsArgs      = 9  // DiagnosticsArgs
	ESM_RequestContextArgs   = 10 // RequestContextArgs
	ESM_McpArgs              = 11 // McpArgs
	ESM_ShellStreamArgs      = 14 // ShellArgs (stream variant)
	ESM_BackgroundShellSpawn = 16 // BackgroundShellSpawnArgs
	ESM_FetchArgs            = 20 // FetchArgs
	ESM_WriteShellStdinArgs  = 23 // WriteShellStdinArgs
	// Modern exec requests (senpi dispatch table):
	ESM_ListMcpResourcesArgs          = 17 // ListMcpResourcesExecArgs
	ESM_ReadMcpResourceArgs           = 18 // ReadMcpResourceExecArgs
	ESM_RecordScreenArgs              = 21 // RecordScreenArgs
	ESM_ComputerUseArgs               = 22 // ComputerUseArgs
	ESM_ExecuteHookArgs               = 27 // ExecuteHookArgs
	ESM_SubagentArgs                  = 28 // SubagentArgs
	ESM_RedactedReadArgs              = 29 // ReadArgs (redacted variant)
	ESM_ForceBackgroundShellArgs      = 30 // ForceBackgroundShellArgs
	ESM_ForceBackgroundSubagentArgs   = 31 // ForceBackgroundSubagentArgs
	ESM_McpStateArgs                  = 36 // McpStateExecArgs
	ESM_SubagentAwaitArgs             = 37 // SubagentAwaitArgs
	ESM_SmartModeClassifierArgs       = 38 // SmartModeClassifierArgs
	ESM_CanvasDiagnosticsArgs         = 40 // CanvasDiagnosticsArgs
	ESM_ShellAllowlistPrecheckArgs    = 41 // ShellAllowlistPrecheckArgs
	ESM_McpAllowlistPrecheckArgs      = 42 // McpAllowlistPrecheckArgs
	ESM_WebFetchAllowlistPrecheckArgs = 43 // WebFetchAllowlistPrecheckArgs
	ESM_GitDiffRequest                = 44 // GetDiffRequest (answer via ExecClientThrow)
	ESM_PiReadArgs                    = 45 // PiReadExecArgs
	ESM_PiBashArgs                    = 46 // PiBashExecArgs
	ESM_PiEditArgs                    = 47 // PiEditExecArgs
	ESM_PiWriteArgs                   = 48 // PiWriteExecArgs
	ESM_PiGrepArgs                    = 49 // PiGrepExecArgs
	ESM_PiFindArgs                    = 50 // PiFindExecArgs
	ESM_PiLsArgs                      = 51 // PiLsExecArgs
	ESM_MiniSweAgentBashArgs          = 52 // ShellArgs (mini-SWE-agent variant)
	ESM_ConversationSearchArgs        = 53 // ConversationSearchArgs
	ESM_AgentStoreConflictArgs        = 54 // AgentStoreConflictArgs
)

// ExecClientMessage
const (
	ECM_Id     = 1  // uint32
	ECM_ExecId = 15 // string
	// oneof message (mirrors server fields):
	ECM_ShellResult             = 2
	ECM_WriteResult             = 3
	ECM_DeleteResult            = 4
	ECM_GrepResult              = 5
	ECM_ReadResult              = 7
	ECM_LsResult                = 8
	ECM_DiagnosticsResult       = 9
	ECM_RequestContextResult    = 10
	ECM_McpResult               = 11
	ECM_ShellStream             = 14
	ECM_BackgroundShellSpawnRes = 16
	ECM_FetchResult             = 20
	ECM_WriteShellStdinResult   = 23
	// Modern exec results (note: pi_* results shift +1 vs. their request
	// numbers; there is no client case 45 — that is hook_additional_contexts):
	ECM_ListMcpResourcesResult          = 17 // ListMcpResourcesExecResult
	ECM_ReadMcpResourceResult           = 18 // ReadMcpResourceExecResult
	ECM_RecordScreenResult              = 21 // RecordScreenResult
	ECM_ComputerUseResult               = 22 // ComputerUseResult
	ECM_ExecuteHookResult               = 27 // ExecuteHookResult
	ECM_SubagentResult                  = 28 // SubagentResult
	ECM_RedactedReadResult              = 29 // ReadResult
	ECM_ForceBackgroundShellResult      = 30 // ForceBackgroundShellResult
	ECM_ForceBackgroundSubagentResult   = 31 // ForceBackgroundSubagentResult
	ECM_McpStateResult                  = 36 // McpStateExecResult
	ECM_SubagentAwaitResult             = 37 // SubagentAwaitResult
	ECM_SmartModeClassifierResult       = 38 // SmartModeClassifierResult
	ECM_CanvasDiagnosticsResult         = 40 // CanvasDiagnosticsResult
	ECM_ShellAllowlistPrecheckResult    = 41 // ShellAllowlistPrecheckResult
	ECM_McpAllowlistPrecheckResult      = 42 // McpAllowlistPrecheckResult
	ECM_WebFetchAllowlistPrecheckResult = 43 // WebFetchAllowlistPrecheckResult
	ECM_GitDiffResponse                 = 44 // GetDiffResponse (no error variant; use ExecClientThrow)
	ECM_PiReadResult                    = 46 // PiReadExecResult
	ECM_PiBashResult                    = 47 // PiBashExecResult
	ECM_PiEditResult                    = 48 // PiEditExecResult
	ECM_PiWriteResult                   = 49 // PiWriteExecResult
	ECM_PiGrepResult                    = 50 // PiGrepExecResult
	ECM_PiFindResult                    = 51 // PiFindExecResult
	ECM_PiLsResult                      = 52 // PiLsExecResult
	ECM_ConversationSearchResult        = 53 // ConversationSearchResult
	ECM_AgentStoreConflictResult        = 54 // AgentStoreConflictResult
	ECM_MiniSweAgentBashResult          = 55 // ShellResult
)

// ExecClientControlMessage oneof "message"
const (
	ECCM_StreamClose = 1 // ExecClientStreamClose
	ECCM_Throw       = 2 // ExecClientThrow
	ECCM_Heartbeat   = 3 // ExecClientHeartbeat
)

// ExecClientStreamClose
const (
	ECSC_Id = 1 // uint32
)

// ExecClientThrow
const (
	ECT_Id        = 1 // uint32
	ECT_Error     = 2 // string
	ECT_ErrorCode = 4 // optional string (3 is stack_trace)
)

// ExecClientHeartbeat
const (
	ECHB_Id = 1 // uint32
)

// McpArgs
const (
	MCA_Name               = 1 // string
	MCA_Args               = 2 // map<string, bytes>
	MCA_ToolCallId         = 3 // string
	MCA_ProviderIdentifier = 4 // string
	MCA_ToolName           = 5 // string
)

// RequestContextResult oneof "result"
const (
	RCR_Success = 1 // RequestContextSuccess
	RCR_Error   = 2 // RequestContextError
)

// RequestContextSuccess (msg 337)
const (
	RCS_RequestContext = 1 // RequestContext
)

// RequestContext
const (
	RC_Rules = 2 // repeated CursorRule
	RC_Tools = 7 // repeated McpToolDefinition
)

// McpResult oneof "result"
const (
	MCR_Success  = 1 // McpSuccess
	MCR_Error    = 2 // McpError
	MCR_Rejected = 3 // McpRejected
)

// McpSuccess (msg 290)
const (
	MCS_Content = 1 // repeated McpToolResultContentItem
	MCS_IsError = 2 // bool
)

// McpToolResultContentItem oneof "content"
const (
	MTRCI_Text = 1 // McpTextContent
)

// McpTextContent (msg 287)
const (
	MTC_Text = 1 // string
)

// McpError (msg 291)
const (
	MCE_Error = 1 // string
)

// --- Rejection messages ---

// ReadRejected: path=1, reason=2
// ShellRejected: command=1, workingDirectory=2, reason=3, isReadonly=4
// WriteRejected: path=1, reason=2
// DeleteRejected: path=1, reason=2
// LsRejected: path=1, reason=2
// GrepError: error=1
// FetchError: url=1, error=2
// WriteShellStdinError: error=1

// Result oneof case numbers, verified against senpi's agent.proto.
// (Earlier guesses — ShellResult.rejected=5, WriteResult.rejected=5,
// DeleteResult.rejected=3, BackgroundShellSpawnResult.rejected=2 — were
// wrong; the values below match the schema. The dynamicpb encoders set
// these by field name, so runtime behavior was never affected.)
const (
	RR_Rejected   = 3 // ReadResult.rejected
	SR_Rejected   = 4 // ShellResult.rejected (1=success 2=failure 3=timeout 4=rejected 5=spawn_error 7=permission_denied)
	WR_Rejected   = 6 // WriteResult.rejected (1=success 3=permission_denied 4=no_space 5=error 6=rejected)
	DR_Rejected   = 6 // DeleteResult.rejected
	LR_Rejected   = 3 // LsResult.rejected
	GR_Error      = 2 // GrepResult.error
	FR_Error      = 2 // FetchResult.error
	BSSR_Rejected = 3 // BackgroundShellSpawnResult.rejected
	WSSR_Error    = 2 // WriteShellStdinResult.error
)

// --- Modern exec result message shapes (senpi agent.proto) ---

// ListMcpResourcesExecResult oneof "result": success=1, error=2, rejected=3
const (
	LMR_Success  = 1 // ListMcpResourcesSuccess (empty message = no resources)
	LMR_Error    = 2 // ListMcpResourcesError
	LMR_Rejected = 3 // ListMcpResourcesRejected
	LMRE_Error   = 1 // ListMcpResourcesError.error
	LMRR_Reason  = 1 // ListMcpResourcesRejected.reason
)

// ReadMcpResourceExecResult oneof "result"
const (
	RMR_NotFound = 4 // ReadMcpResourceNotFound
	RMNF_Uri     = 1 // ReadMcpResourceNotFound.uri
)

// RecordScreenResult oneof "result" (failure is the error variant)
const (
	RSCR_Failure = 4 // RecordScreenFailure
	RSF_Error    = 1 // RecordScreenFailure.error
)

// ComputerUseResult oneof "result"
const (
	CUR_Error = 2 // ComputerUseError
	CUE_Error = 1 // ComputerUseError.error
)

// SubagentResult oneof "result"
const (
	SAR_Error   = 2 // SubagentError
	SAE_AgentId = 1 // SubagentError.agent_id (optional)
	SAE_Error   = 2 // SubagentError.error
)

// SubagentAwaitResult oneof "result"
const (
	SAW_NotFound = 3 // SubagentAwaitNotFound
	SAW_Error    = 4 // SubagentAwaitError
	SANF_AgentId = 1 // SubagentAwaitNotFound.agent_id
)

// ForceBackgroundShellResult / ForceBackgroundSubagentResult
const (
	FBS_Status      = 1 // enum ForceBackgroundShellStatus
	FBS_ShellResult = 2 // optional ShellResult
	// enum values (shared numbering for both force-background statuses)
	FBS_STATUS_NotFound = 2
)

// McpStateExecResult oneof "result"
const (
	MSR_Success  = 1 // McpStateSuccess
	MSR_Error    = 2 // McpStateError
	MSR_Rejected = 3 // McpStateRejected
	MSS_Servers  = 1 // McpStateSuccess.servers (repeated McpStateServer)
)

// McpStateServer
const (
	MSTS_ServerName       = 1 // string
	MSTS_ServerIdentifier = 2 // string
	MSTS_Tools            = 5 // repeated McpToolDefinition
	MSTS_Status           = 7 // optional string (e.g. "connected")
)

// SmartModeClassifierResult oneof "result"
const (
	SMCR_Error = 2 // SmartModeClassifierError
	SMCE_Error = 1 // SmartModeClassifierError.error
)

// CanvasDiagnosticsResult oneof "result"
const (
	CDR_Error = 2 // CanvasDiagnosticsError
	CDE_Path  = 1 // CanvasDiagnosticsError.path
	CDE_Error = 2 // CanvasDiagnosticsError.error
)

// Allowlist precheck results (ShellAllowlistPrecheckResult,
// McpAllowlistPrecheckResult, WebFetchAllowlistPrecheckResult share the shape)
const (
	ALP_Allowlisted = 1 // bool
)

// Pi*ExecResult oneof "result" (success=1, error=2; edit/write also have
// rejected=3). Every Pi*ExecError carries error=1; every Pi*ExecRejected
// carries reason=1.
const (
	PI_Success  = 1
	PI_Error    = 2
	PI_Rejected = 3
	PIE_Error   = 1
	PIR_Reason  = 1
)

// ConversationSearchResult / AgentStoreConflictResult oneof "result"
const (
	CSR_Error  = 2 // ConversationSearchError
	CSE_Error  = 1 // ConversationSearchError.error
	ASCR_Error = 2 // AgentStoreConflictError
	ASCE_Error = 1 // AgentStoreConflictError.error
)

// ExecuteHookArgs / ExecuteHookResult
const (
	EHA_Request  = 1 // ExecuteHookRequest
	EHR_Response = 1 // ExecuteHookResponse
)

// ExecuteHookRequest / ExecuteHookResponse oneof cases (identical numbering;
// case 10 is unused, stop=11)
const (
	EXH_PreCompact         = 1
	EXH_SubagentStart      = 2
	EXH_SubagentStop       = 3
	EXH_PreToolUse         = 4
	EXH_PostToolUse        = 5
	EXH_PostToolUseFailure = 6
	EXH_BeforeSubmitPrompt = 7
	EXH_AfterAgentResponse = 8
	EXH_AfterAgentThought  = 9
	EXH_Stop               = 11
)

// ReadResult error variant (used for redacted_read answers)
const (
	RR_Error  = 2 // ReadResult.error
	RER_Path  = 1 // ReadError.path
	RER_Error = 2 // ReadError.error
)

// --- Rejection struct fields ---
const (
	REJ_Path        = 1
	REJ_Reason      = 2
	SREJ_Command    = 1
	SREJ_WorkingDir = 2
	SREJ_Reason     = 3
	SREJ_IsReadonly = 4
	GERR_Error      = 1
	FERR_Url        = 1
	FERR_Error      = 2
)

// ReadArgs
const (
	RA_Path = 1 // string
)

// WriteArgs
const (
	WA_Path = 1 // string
)

// DeleteArgs
const (
	DA_Path = 1 // string
)

// LsArgs
const (
	LA_Path = 1 // string
)

// ShellArgs
const (
	SHA_Command          = 1 // string
	SHA_WorkingDirectory = 2 // string
)

// FetchArgs
const (
	FA_Url = 1 // string
)

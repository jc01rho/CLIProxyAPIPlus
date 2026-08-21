package proto

import (
	"encoding/hex"
	"fmt"

	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protowire"
)

// ServerMessageType identifies the kind of decoded server message.
type ServerMessageType int

const (
	ServerMsgUnknown             ServerMessageType = iota
	ServerMsgTextDelta                             // Text content delta
	ServerMsgThinkingDelta                         // Thinking/reasoning delta
	ServerMsgThinkingCompleted                     // Thinking completed
	ServerMsgKvGetBlob                             // Server wants a blob
	ServerMsgKvSetBlob                             // Server wants to store a blob
	ServerMsgExecRequestCtx                        // Server requests context (tools, etc.)
	ServerMsgExecMcpArgs                           // Server wants MCP tool execution
	ServerMsgExecShellArgs                         // Rejected: shell command
	ServerMsgExecReadArgs                          // Rejected: file read
	ServerMsgExecWriteArgs                         // Rejected: file write
	ServerMsgExecDeleteArgs                        // Rejected: file delete
	ServerMsgExecLsArgs                            // Rejected: directory listing
	ServerMsgExecGrepArgs                          // Rejected: grep search
	ServerMsgExecFetchArgs                         // Rejected: HTTP fetch
	ServerMsgExecDiagnostics                       // Respond with empty diagnostics
	ServerMsgExecShellStream                       // Rejected: shell stream
	ServerMsgExecBgShellSpawn                      // Rejected: background shell
	ServerMsgExecWriteShellStdin                   // Rejected: write shell stdin
	ServerMsgExecOther                             // Other exec types (respond with empty)
	ServerMsgToolCallStarted                       // ToolCallStartedUpdate
	ServerMsgToolCallCompleted                     // ToolCallCompletedUpdate
	ServerMsgPartialToolCall                       // PartialToolCallUpdate (args text snapshot)
	ServerMsgToolCallDelta                         // ToolCallDeltaUpdate
	ServerMsgTurnEnded                             // Turn has ended (no more output)
	ServerMsgHeartbeat                             // Server heartbeat
	ServerMsgTokenDelta                            // Token usage delta
	ServerMsgCheckpoint                            // Conversation checkpoint update
)

// DecodedServerMessage holds parsed data from an AgentServerMessage.
type DecodedServerMessage struct {
	Type ServerMessageType

	// For text/thinking deltas
	Text string

	// For KV messages
	KvId     uint32
	BlobId   []byte // hex-encoded blob ID
	BlobData []byte // for setBlobArgs

	// For exec messages
	ExecMsgId uint32
	ExecId    string

	// For MCP args
	McpToolName   string
	McpToolCallId string
	McpArgs       map[string][]byte // arg name -> protobuf-encoded value

	// For tool call streaming updates. CallId is the ToolCall*Update envelope
	// correlation id; ToolCallId and ToolCallCase come from the nested ToolCall
	// (modern builds carry the call id on the envelope, field 57).
	CallId            string // ToolCall*Update.call_id
	ToolCallId        string // ToolCall.tool_call_id
	ToolCallCase      int    // ToolCall.tool oneof case number (0 when unset)
	ArgsTextDelta     string // PartialToolCallUpdate.args_text_delta (cumulative snapshot)
	ToolCallDeltaData []byte // raw ToolCallDelta bytes (ToolCallDeltaUpdate)

	// For rejection context
	Path             string
	Command          string
	WorkingDirectory string
	Url              string

	// For other exec - the raw field number for building a response
	ExecFieldNumber int

	// For TokenDeltaUpdate
	TokenDelta int64

	// For TurnEndedUpdate billed tokens (production schema 2026.08.11)
	TurnEndedInput      *int64
	TurnEndedOutput     *int64
	TurnEndedCacheRead  *int64
	TurnEndedCacheWrite *int64

	// For conversation checkpoint update (raw bytes, not decoded)
	CheckpointData []byte
	// CheckpointUsedTokens is the tokenDetails.usedTokens varint pulled from
	// the raw ConversationStateStructure (field 5 → CTD_UsedTokens).
	CheckpointUsedTokens int64
}

// DecodeAgentServerMessage parses an AgentServerMessage and returns
// a structured representation of the first meaningful message found.
func DecodeAgentServerMessage(data []byte) (*DecodedServerMessage, error) {
	msg := &DecodedServerMessage{Type: ServerMsgUnknown}

	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return msg, fmt.Errorf("invalid tag")
		}
		data = data[n:]

		switch typ {
		case protowire.BytesType:
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return msg, fmt.Errorf("invalid bytes field %d", num)
			}
			data = data[n:]

			// Debug: log top-level ASM fields
			log.Debugf("DecodeAgentServerMessage: found ASM field %d, len=%d", num, len(val))

			switch num {
			case ASM_InteractionUpdate:
				log.Debugf("DecodeAgentServerMessage: calling decodeInteractionUpdate")
				decodeInteractionUpdate(val, msg)
			case ASM_ExecServerMessage:
				log.Debugf("DecodeAgentServerMessage: calling decodeExecServerMessage")
				decodeExecServerMessage(val, msg)
			case ASM_KvServerMessage:
				decodeKvServerMessage(val, msg)
			case ASM_ConversationCheckpoint:
				msg.Type = ServerMsgCheckpoint
				msg.CheckpointData = append([]byte(nil), val...) // copy raw bytes
				msg.CheckpointUsedTokens = decodeCheckpointUsedTokens(val)
				log.Debugf("DecodeAgentServerMessage: captured checkpoint %d bytes, used_tokens=%d", len(val), msg.CheckpointUsedTokens)
			}

		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return msg, fmt.Errorf("invalid varint field %d", num)
			}
			data = data[n:]

		default:
			// Skip unknown wire types
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return msg, fmt.Errorf("invalid field %d", num)
			}
			data = data[n:]
		}
	}

	return msg, nil
}

func decodeInteractionUpdate(data []byte, msg *DecodedServerMessage) {
	log.Debugf("decodeInteractionUpdate: input len=%d, hex=%x", len(data), data)
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			log.Debugf("decodeInteractionUpdate: invalid tag, remaining=%x", data)
			return
		}
		data = data[n:]
		log.Debugf("decodeInteractionUpdate: field=%d wire=%d remaining=%d bytes", num, typ, len(data))

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				log.Debugf("decodeInteractionUpdate: invalid bytes field %d", num)
				return
			}
			data = data[n:]
			log.Debugf("decodeInteractionUpdate: field %d content len=%d, first 20 bytes: %x", num, len(val), val[:min(20, len(val))])

			switch num {
			case IU_TextDelta:
				msg.Type = ServerMsgTextDelta
				msg.Text = decodeStringField(val, TDU_Text)
				log.Debugf("decodeInteractionUpdate: TextDelta text=%q", msg.Text)
			case IU_ThinkingDelta:
				msg.Type = ServerMsgThinkingDelta
				msg.Text = decodeStringField(val, TKD_Text)
				log.Debugf("decodeInteractionUpdate: ThinkingDelta text=%q", msg.Text)
			case IU_ThinkingCompleted:
				msg.Type = ServerMsgThinkingCompleted
				log.Debugf("decodeInteractionUpdate: ThinkingCompleted")
			case IU_ToolCallStarted:
				msg.Type = ServerMsgToolCallStarted
				decodeToolCallUpdate(val, msg)
				log.Debugf("decodeInteractionUpdate: ToolCallStarted call_id=%q tool_call_id=%q case=%d", msg.CallId, msg.ToolCallId, msg.ToolCallCase)
			case IU_ToolCallCompleted:
				msg.Type = ServerMsgToolCallCompleted
				decodeToolCallUpdate(val, msg)
				log.Debugf("decodeInteractionUpdate: ToolCallCompleted call_id=%q tool_call_id=%q case=%d", msg.CallId, msg.ToolCallId, msg.ToolCallCase)
			case IU_PartialToolCall:
				msg.Type = ServerMsgPartialToolCall
				decodeToolCallUpdate(val, msg)
				log.Debugf("decodeInteractionUpdate: PartialToolCall call_id=%q args_len=%d", msg.CallId, len(msg.ArgsTextDelta))
			case IU_ToolCallDelta:
				msg.Type = ServerMsgToolCallDelta
				decodeToolCallDeltaUpdate(val, msg)
				log.Debugf("decodeInteractionUpdate: ToolCallDelta call_id=%q delta_len=%d", msg.CallId, len(msg.ToolCallDeltaData))
			case IU_TokenDelta:
				// token_delta - extract token count
				msg.Type = ServerMsgTokenDelta
				msg.TokenDelta = decodeVarintField(val, TKDU_Tokens)
				log.Debugf("decodeInteractionUpdate: TokenDeltaUpdate tokens=%d", msg.TokenDelta)
			case IU_Heartbeat:
				// heartbeat from server
				msg.Type = ServerMsgHeartbeat
			case IU_TurnEnded:
				msg.Type = ServerMsgTurnEnded
				decodeTurnEndedUpdate(val, msg)
				log.Debugf("decodeInteractionUpdate: TurnEndedUpdate billed=%v out=%v cr=%v cw=%v - stream should end", msg.TurnEndedInput, msg.TurnEndedOutput, msg.TurnEndedCacheRead, msg.TurnEndedCacheWrite)
			case IU_StepStarted:
				// step_started - ignore
				log.Debugf("decodeInteractionUpdate: StepStartedUpdate (ignored)")
			case IU_StepCompleted:
				// step_completed - ignore
				log.Debugf("decodeInteractionUpdate: StepCompletedUpdate (ignored)")
			default:
				log.Debugf("decodeInteractionUpdate: unknown field %d", num)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

// decodeToolCallUpdate parses the shared envelope of ToolCallStartedUpdate,
// ToolCallCompletedUpdate and PartialToolCallUpdate (call_id=1, tool_call=2,
// plus args_text_delta=3 on the partial variant).
func decodeToolCallUpdate(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]
			switch num {
			case TCU_CallId:
				msg.CallId = string(val)
			case TCU_ToolCall:
				decodeToolCall(val, msg)
			case PTCU_ArgsTextDelta:
				msg.ArgsTextDelta = string(val)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

// decodeToolCall extracts the envelope tool_call_id (field 57) and the oneof
// tool-variant case number from a ToolCall. The variant's args are left for
// the caller's own tool dispatch; every oneof variant is a message field.
func decodeToolCall(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]
			if num == TC_ToolCallId {
				msg.ToolCallId = string(val)
			} else {
				msg.ToolCallCase = int(num)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

// decodeToolCallDeltaUpdate parses a ToolCallDeltaUpdate: call_id=1,
// tool_call_delta=2. The delta submessage (Shell/Task/Edit variants, each
// wrapping a nested InteractionUpdate) is kept raw for the caller.
func decodeToolCallDeltaUpdate(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]
			switch num {
			case TCDU_CallId:
				msg.CallId = string(val)
			case TCDU_ToolCallDelta:
				msg.ToolCallDeltaData = append([]byte(nil), val...)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeKvServerMessage(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		switch typ {
		case protowire.VarintType:
			val, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return
			}
			data = data[n:]
			if num == KSM_Id {
				msg.KvId = uint32(val)
			}

		case protowire.BytesType:
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]

			switch num {
			case KSM_GetBlobArgs:
				msg.Type = ServerMsgKvGetBlob
				msg.BlobId = decodeBytesField(val, GBA_BlobId)
			case KSM_SetBlobArgs:
				msg.Type = ServerMsgKvSetBlob
				decodeSetBlobArgs(val, msg)
			}

		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeSetBlobArgs(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]
			switch num {
			case SBA_BlobId:
				msg.BlobId = val
			case SBA_BlobData:
				msg.BlobData = val
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeExecServerMessage(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		switch typ {
		case protowire.VarintType:
			val, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return
			}
			data = data[n:]
			if num == ESM_Id {
				msg.ExecMsgId = uint32(val)
				log.Debugf("decodeExecServerMessage: ESM_Id = %d", val)
			}

		case protowire.BytesType:
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]

			// Debug: log all fields found in ExecServerMessage
			log.Debugf("decodeExecServerMessage: found field %d, len=%d, first 20 bytes: %x", num, len(val), val[:min(20, len(val))])

			switch num {
			case ESM_ExecId:
				msg.ExecId = string(val)
				log.Debugf("decodeExecServerMessage: ESM_ExecId = %q", msg.ExecId)
			case ESM_RequestContextArgs:
				msg.Type = ServerMsgExecRequestCtx
			case ESM_McpArgs:
				msg.Type = ServerMsgExecMcpArgs
				decodeMcpArgs(val, msg)
			case ESM_ShellArgs:
				msg.Type = ServerMsgExecShellArgs
				decodeShellArgs(val, msg)
			case ESM_ShellStreamArgs:
				msg.Type = ServerMsgExecShellStream
				decodeShellArgs(val, msg)
			case ESM_ReadArgs:
				msg.Type = ServerMsgExecReadArgs
				msg.Path = decodeStringField(val, RA_Path)
			case ESM_WriteArgs:
				msg.Type = ServerMsgExecWriteArgs
				msg.Path = decodeStringField(val, WA_Path)
			case ESM_DeleteArgs:
				msg.Type = ServerMsgExecDeleteArgs
				msg.Path = decodeStringField(val, DA_Path)
			case ESM_LsArgs:
				msg.Type = ServerMsgExecLsArgs
				msg.Path = decodeStringField(val, LA_Path)
			case ESM_GrepArgs:
				msg.Type = ServerMsgExecGrepArgs
			case ESM_FetchArgs:
				msg.Type = ServerMsgExecFetchArgs
				msg.Url = decodeStringField(val, FA_Url)
			case ESM_DiagnosticsArgs:
				msg.Type = ServerMsgExecDiagnostics
			case ESM_BackgroundShellSpawn:
				msg.Type = ServerMsgExecBgShellSpawn
				decodeShellArgs(val, msg) // same structure
			case ESM_WriteShellStdinArgs:
				msg.Type = ServerMsgExecWriteShellStdin
			default:
				// Unknown exec types - only set if we haven't identified the type yet
				// (other fields like span_context (19) come after the exec type field)
				if msg.Type == ServerMsgUnknown {
					msg.Type = ServerMsgExecOther
					msg.ExecFieldNumber = int(num)
				}
			}

		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeMcpArgs(data []byte, msg *DecodedServerMessage) {
	msg.McpArgs = make(map[string][]byte)
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]

			switch num {
			case MCA_Name:
				msg.McpToolName = string(val)
			case MCA_Args:
				// Map entries are encoded as submessages with key=1, value=2
				decodeMapEntry(val, msg.McpArgs)
			case MCA_ToolCallId:
				msg.McpToolCallId = string(val)
			case MCA_ToolName:
				// ToolName takes precedence if present
				if msg.McpToolName == "" || string(val) != "" {
					msg.McpToolName = string(val)
				}
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

func decodeMapEntry(data []byte, m map[string][]byte) {
	var key string
	var value []byte
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]
			if num == 1 {
				key = string(val)
			} else if num == 2 {
				value = append([]byte(nil), val...)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
	if key != "" {
		m[key] = value
	}
}

func decodeShellArgs(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return
			}
			data = data[n:]
			switch num {
			case SHA_Command:
				msg.Command = string(val)
			case SHA_WorkingDirectory:
				msg.WorkingDirectory = string(val)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
		}
	}
}

// --- Helper decoders ---

// decodeStringField extracts a string from the first matching field in a submessage.
func decodeStringField(data []byte, targetField protowire.Number) string {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return ""
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return ""
			}
			data = data[n:]
			if num == targetField {
				return string(val)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return ""
			}
			data = data[n:]
		}
	}
	return ""
}

// decodeBytesField extracts bytes from the first matching field in a submessage.
func decodeBytesField(data []byte, targetField protowire.Number) []byte {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil
		}
		data = data[n:]

		if typ == protowire.BytesType {
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
			if num == targetField {
				return append([]byte(nil), val...)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		}
	}
	return nil
}

func decodeTurnEndedUpdate(data []byte, msg *DecodedServerMessage) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			val, m := protowire.ConsumeVarint(data)
			if m < 0 {
				return
			}
			data = data[m:]
			v := int64(val)
			switch num {
			case TEU_InputTokens:
				msg.TurnEndedInput = &v
			case TEU_OutputTokens:
				msg.TurnEndedOutput = &v
			case TEU_CacheReadTokens:
				msg.TurnEndedCacheRead = &v
			case TEU_CacheWriteTokens:
				msg.TurnEndedCacheWrite = &v
			}
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return
			}
			data = data[m:]
		}
	}
}

// decodeCheckpointUsedTokens extracts tokenDetails.usedTokens (CTD field inside
// ConversationStateStructure field 5) from a raw checkpoint bytes payload.
func decodeCheckpointUsedTokens(checkpointData []byte) int64 {
	for len(checkpointData) > 0 {
		num, typ, n := protowire.ConsumeTag(checkpointData)
		if n < 0 {
			return 0
		}
		checkpointData = checkpointData[n:]
		if num == CSS_TokenDetails && typ == protowire.BytesType {
			inner, m := protowire.ConsumeBytes(checkpointData)
			if m < 0 {
				return 0
			}
			for len(inner) > 0 {
				subNum, subTyp, k := protowire.ConsumeTag(inner)
				if k < 0 {
					return 0
				}
				inner = inner[k:]
				if subNum == CTD_UsedTokens && subTyp == protowire.VarintType {
					v, p := protowire.ConsumeVarint(inner)
					if p < 0 {
						return 0
					}
					return int64(v)
				}
				m2 := protowire.ConsumeFieldValue(subNum, subTyp, inner)
				if m2 < 0 {
					return 0
				}
				inner = inner[m2:]
			}
			return 0
		}
		m := protowire.ConsumeFieldValue(num, typ, checkpointData)
		if m < 0 {
			return 0
		}
		checkpointData = checkpointData[m:]
	}
	return 0
}

// decodeVarintField extracts an int64 from the first matching varint field in a submessage.
func decodeVarintField(data []byte, targetField protowire.Number) int64 {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return 0
		}
		data = data[n:]
		if typ == protowire.VarintType {
			val, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return 0
			}
			data = data[n:]
			if num == targetField {
				return int64(val)
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return 0
			}
			data = data[n:]
		}
	}
	return 0
}

// BlobIdHex returns the hex string of a blob ID for use as a map key.
func BlobIdHex(blobId []byte) string {
	return hex.EncodeToString(blobId)
}

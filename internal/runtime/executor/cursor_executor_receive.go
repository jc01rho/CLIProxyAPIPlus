package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	log "github.com/sirupsen/logrus"
)

const (
	cursorExecHeartbeatInterval = 3 * time.Second
	// cursorStreamStallThreshold mirrors senpi's
	// CURSOR_STREAM_HEALTH_FAIL_THRESHOLD_MS = 30_000 (cursor-agent.ts:258):
	// maximum inbound silence before an attempt without turnEnded is failed.
	cursorStreamStallThreshold = 30 * time.Second
)

type cursorToolCallUpdateKind int

const (
	cursorToolCallStarted cursorToolCallUpdateKind = iota + 1
	cursorToolCallArgumentsDelta
	cursorToolCallCompleted
)

type cursorToolCallUpdate struct {
	Kind           cursorToolCallUpdateKind
	CallID         string
	ToolCallID     string
	ToolName       string
	ArgumentsDelta string
}

type cursorExecRequest struct {
	caseNum int
	args    []byte
}

type cursorPendingMcp struct {
	exec      pendingMcpExec
	stopPulse func()
}

type cursorStreamedToolState struct {
	callID     string
	toolCallID string
	toolName   string
	args       string
}

// cursorStreamConn is the transport surface the server-frame dispatcher needs;
// *cursorproto.H2Stream satisfies it. Tests substitute an in-memory fake.
type cursorStreamConn interface {
	ID() string
	Data() <-chan []byte
	Err() error
	Write(data []byte) error
}

// cursorStallWatchdog is the inbound-frame deadline for one stream attempt:
// fired becomes receivable when no inbound frame has arrived within the
// threshold of the last kick. Any server frame on Data() — including
// heartbeats — counts as liveness and re-arms the deadline, matching senpi's
// armStreamHealthTimer (cursor-agent.ts:630-660). The zero value is a disabled
// watchdog; tests substitute manual channels to drive the deadline without
// wall-clock sleeps.
type cursorStallWatchdog struct {
	fired <-chan time.Time
	kick  func()
	stop  func()
}

// newCursorStallWatchdog arms a timer-backed watchdog. A non-positive timeout
// takes the 30s senpi default.
func newCursorStallWatchdog(timeout time.Duration) cursorStallWatchdog {
	if timeout <= 0 {
		timeout = cursorStreamStallThreshold
	}
	timer := time.NewTimer(timeout)
	return cursorStallWatchdog{
		fired: timer.C,
		kick:  func() { timer.Reset(timeout) },
		stop:  func() { timer.Stop() },
	}
}

// kickInboundFrame re-arms the deadline after any inbound frame. It is a no-op
// once the watchdog is disabled or disarmed.
func (w *cursorStallWatchdog) kickInboundFrame() {
	if w.kick != nil {
		w.kick()
	}
}

// disarm permanently stops the deadline once turnEnded has been seen — senpi
// stops the health timer at the same point, so the client-driven MCP result
// wait is never bounded by the watchdog. A fire already buffered in the
// channel is neutralized by the turnEndedSeen guard in the receive select.
func (w *cursorStallWatchdog) disarm() {
	if w.stop != nil {
		w.stop()
		w.stop = nil
	}
	w.kick = nil
}

// processH2SessionFrames mirrors Senpi's server-frame dispatcher. It keeps the
// Connect stream active while MCP work is outstanding, but all other exec
// variants are answered immediately with a typed result and stream_close.
func processH2SessionFrames(
	ctx context.Context,
	stream cursorStreamConn,
	blobStore map[string][]byte,
	mcpTools []cursorproto.McpToolDef,
	onText func(text string, isThinking bool),
	onToolCall func(update cursorToolCallUpdate),
	onMcpExec func(exec pendingMcpExec),
	toolResultCh <-chan []toolResultInfo,
	tokenUsage *cursorTokenUsage,
	onCheckpoint func(data []byte),
	stall cursorStallWatchdog,
) error {
	var buf bytes.Buffer
	var pendingMcp *cursorPendingMcp
	turnEndedSeen := false
	streamedTools := make(map[string]*cursorStreamedToolState)

	stopPendingHeartbeat := func() {
		if pendingMcp != nil && pendingMcp.stopPulse != nil {
			pendingMcp.stopPulse()
		}
	}
	defer stopPendingHeartbeat()
	defer stall.disarm()

	log.Debugf("cursor: processH2SessionFrames started for streamID=%s, waiting for data...", stream.ID())
	for {
		var resultsCh <-chan []toolResultInfo
		if pendingMcp != nil {
			resultsCh = toolResultCh
		}

		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-stall.fired:
			// turnEnded already seen: a fire buffered before the disarm is
			// stale, mirroring senpi's sawTurnEnded guard inside the health
			// timer callback (cursor-agent.ts:641-644).
			if turnEndedSeen {
				continue
			}
			return &cursorRetryableStreamError{
				msg:   "Cursor stream ended before turnEnded: inbound stream stalled",
				cause: cursorStreamRetryCauseStall,
			}

		case toolResults, ok := <-resultsCh:
			if !ok {
				return nil
			}
			matched := false
			for _, tr := range toolResults {
				if tr.ToolCallId != pendingMcp.exec.ToolCallId {
					continue
				}
				matched = true
				result := cursorproto.EncodeExecMcpResult(pendingMcp.exec.ExecMsgId, pendingMcp.exec.ExecId, tr.Content, false)
				if err := cursorWriteClientMessage(stream, result); err != nil {
					return err
				}
				break
			}
			if !matched {
				result := cursorproto.EncodeExecMcpError(pendingMcp.exec.ExecMsgId, pendingMcp.exec.ExecId, "MCP tool result was not returned by the proxy client")
				if err := cursorWriteClientMessage(stream, result); err != nil {
					return err
				}
			}
			stopPendingHeartbeat()
			if err := cursorWriteClientMessage(stream, cursorproto.EncodeExecControlStreamClose(pendingMcp.exec.ExecMsgId)); err != nil {
				return err
			}
			pendingMcp = nil
			if turnEndedSeen {
				return nil
			}

		case data, ok := <-stream.Data():
			// The transport's readLoop closes Data when it exits, so channel
			// closure is the single end-of-stream signal (Done() would only
			// preempt frames still buffered in Data).
			if !ok {
				return cursorStreamEndResult(turnEndedSeen, stream.Err())
			}
			// Any inbound frame — heartbeat included — is liveness: re-arm the
			// stall deadline before parsing (senpi resets lastInboundFrameAt on
			// every h2 data chunk, cursor-agent.ts:714).
			stall.kickInboundFrame()
			buf.Write(data)

			for {
				flags, payload, consumed, complete := cursorproto.ParseConnectFrame(buf.Bytes())
				if !complete {
					break
				}
				buf.Next(consumed)

				if flags&cursorproto.ConnectEndStreamFlag != 0 {
					if err := cursorproto.ParseConnectEndStream(payload); err != nil {
						return err
					}
					continue
				}

				msg, err := cursorproto.DecodeAgentServerMessage(payload)
				if err != nil {
					log.Debugf("cursor: failed to decode server message: %v", err)
					continue
				}

				switch msg.Type {
				case cursorproto.ServerMsgTextDelta:
					if msg.Text != "" && onText != nil {
						onText(msg.Text, false)
					}
				case cursorproto.ServerMsgThinkingDelta:
					if msg.Text != "" && onText != nil {
						onText(msg.Text, true)
					}
				case cursorproto.ServerMsgThinkingCompleted, cursorproto.ServerMsgHeartbeat:
					// The OpenAI-compatible caller owns block presentation; no wire response is required.
				case cursorproto.ServerMsgToolCallStarted, cursorproto.ServerMsgPartialToolCall,
					cursorproto.ServerMsgToolCallDelta, cursorproto.ServerMsgToolCallCompleted:
					handleCursorToolCallUpdate(payload, msg, streamedTools, onToolCall)
				case cursorproto.ServerMsgTokenDelta:
					if tokenUsage != nil {
						tokenUsage.addOutput(msg.TokenDelta)
					}
				case cursorproto.ServerMsgCheckpoint:
					if onCheckpoint != nil && len(msg.CheckpointData) > 0 {
						onCheckpoint(msg.CheckpointData)
					}
					if tokenUsage != nil && len(msg.CheckpointData) > 0 {
						tokenUsage.applyCheckpointTokenDetails(msg.CheckpointUsedTokens)
					}
				case cursorproto.ServerMsgKvGetBlob:
					data := blobStore[cursorproto.BlobIdHex(msg.BlobId)]
					if err := cursorWriteClientMessage(stream, cursorproto.EncodeKvGetBlobResult(msg.KvId, data)); err != nil {
						return err
					}
				case cursorproto.ServerMsgKvSetBlob:
					if blobStore != nil {
						blobStore[cursorproto.BlobIdHex(msg.BlobId)] = append([]byte(nil), msg.BlobData...)
					}
					if err := cursorWriteClientMessage(stream, cursorproto.EncodeKvSetBlobResult(msg.KvId)); err != nil {
						return err
					}
				case cursorproto.ServerMsgTurnEnded:
					turnEndedSeen = true
					// The inbound stall deadline ends with the turn: the MCP result
					// wait is client-driven, not watchdog-bounded (senpi disarms the
					// health timer once turnEnded arrives).
					stall.disarm()
					// The billed split (input/output/cacheRead/cacheWrite) applies on
					// EVERY turnEnded — including the normal no-MCP path — mirroring
					// senpi cursor-agent.ts:3591-3594.
					if tokenUsage != nil {
						tokenUsage.applyBilledTurnEndedUsage(msg.TurnEndedInput, msg.TurnEndedOutput, msg.TurnEndedCacheRead, msg.TurnEndedCacheWrite)
					}
					if pendingMcp == nil {
						return nil
					}
				default:
					req, ok := cursorExecRequestFromPayload(payload)
					if !ok {
						continue
					}
					if req.caseNum == cursorproto.ESM_McpArgs {
						if pendingMcp != nil {
							if err := cursorWriteClientMessage(stream, cursorproto.EncodeExecControlThrow(msg.ExecMsgId, "Concurrent MCP exec frames are not supported by this proxy", "exec_dispatch_failed")); err != nil {
								return err
							}
							if err := cursorWriteClientMessage(stream, cursorproto.EncodeExecControlStreamClose(msg.ExecMsgId)); err != nil {
								return err
							}
							continue
						}
						if onMcpExec == nil {
							if err := cursorWriteClientMessage(stream, cursorproto.EncodeExecMcpError(msg.ExecMsgId, msg.ExecId, "MCP relay is unavailable for this request")); err != nil {
								return err
							}
							if err := cursorWriteClientMessage(stream, cursorproto.EncodeExecControlStreamClose(msg.ExecMsgId)); err != nil {
								return err
							}
							continue
						}

						toolCallID := msg.McpToolCallId
						if toolCallID == "" {
							toolCallID = uuid.New().String()
						}
						exec := pendingMcpExec{
							ExecMsgId:  msg.ExecMsgId,
							ExecId:     msg.ExecId,
							ToolCallId: toolCallID,
							ToolName:   msg.McpToolName,
							Args:       decodeMcpArgsToJSON(msg.McpArgs),
						}
						onMcpExec(exec)
						if toolResultCh == nil {
							return nil
						}
						pendingMcp = &cursorPendingMcp{
							exec:      exec,
							stopPulse: startCursorExecHeartbeat(ctx, stream, msg.ExecMsgId),
						}
						continue
					}

					if err := dispatchCursorExec(ctx, stream, msg, req, mcpTools); err != nil {
						return err
					}
				}
			}
		}
	}
}

// cursorStreamEndResult converts the transport's end condition into senpi's
// typed outcome (cursor-agent.ts:770-774): a clean close before turnEnded is
// the retryable clean-end failure, never a bogus success. A transport error
// (RST_STREAM/GOAWAY) passes through unchanged.
func cursorStreamEndResult(turnEndedSeen bool, err error) error {
	if err != nil {
		return err
	}
	if !turnEndedSeen {
		return &cursorRetryableStreamError{
			msg:   "Cursor stream ended before turnEnded",
			cause: cursorStreamRetryCauseCleanEnd,
		}
	}
	return nil
}

func handleCursorToolCallUpdate(
	payload []byte,
	msg *cursorproto.DecodedServerMessage,
	states map[string]*cursorStreamedToolState,
	emit func(cursorToolCallUpdate),
) {
	if emit == nil || msg.ToolCallCase != 15 { // MCP is the only downstream-executable streamed call in CPA.
		return
	}
	key := msg.ToolCallId
	if key == "" {
		key = msg.CallId
	}
	state := states[key]
	if state == nil && msg.CallId != "" {
		state = states[msg.CallId]
	}
	if state != nil {
		if msg.ToolCallId != "" {
			state.toolCallID = msg.ToolCallId
			states[msg.ToolCallId] = state
		}
		if msg.CallId != "" {
			state.callID = msg.CallId
			states[msg.CallId] = state
		}
		if state.toolName == "" {
			state.toolName = cursorMcpToolNameFromServerPayload(payload, msg.Type)
		}
	}

	ensureStarted := func() *cursorStreamedToolState {
		if state != nil {
			return state
		}
		state = &cursorStreamedToolState{
			callID:     msg.CallId,
			toolCallID: msg.ToolCallId,
			toolName:   cursorMcpToolNameFromServerPayload(payload, msg.Type),
		}
		if key != "" {
			states[key] = state
		}
		if msg.CallId != "" {
			states[msg.CallId] = state
		}
		if msg.ToolCallId != "" {
			states[msg.ToolCallId] = state
		}
		emit(cursorToolCallUpdate{
			Kind:       cursorToolCallStarted,
			CallID:     state.callID,
			ToolCallID: state.toolCallID,
			ToolName:   state.toolName,
		})
		return state
	}

	switch msg.Type {
	case cursorproto.ServerMsgToolCallStarted:
		ensureStarted()
	case cursorproto.ServerMsgPartialToolCall:
		state = ensureStarted()
		snapshot := msg.ArgsTextDelta
		chunk := snapshot
		if strings.HasPrefix(snapshot, state.args) {
			chunk = strings.TrimPrefix(snapshot, state.args)
		}
		if chunk == "" {
			return
		}
		state.args += chunk
		emit(cursorToolCallUpdate{
			Kind:           cursorToolCallArgumentsDelta,
			CallID:         state.callID,
			ToolCallID:     state.toolCallID,
			ArgumentsDelta: chunk,
		})
	case cursorproto.ServerMsgToolCallDelta:
		// ToolCallDelta carries nested shell/task/edit interaction updates rather
		// than argument JSON. It is intentionally consumed without fabricating an
		// OpenAI function-argument fragment.
		ensureStarted()
	case cursorproto.ServerMsgToolCallCompleted:
		state = ensureStarted()
		emit(cursorToolCallUpdate{
			Kind:       cursorToolCallCompleted,
			CallID:     state.callID,
			ToolCallID: state.toolCallID,
			ToolName:   state.toolName,
		})
	}
}

func cursorMcpToolNameFromServerPayload(payload []byte, messageType cursorproto.ServerMessageType) string {
	interaction, ok := cursorBytesField(payload, cursorproto.ASM_InteractionUpdate)
	if !ok {
		return ""
	}
	var updateField int
	switch messageType {
	case cursorproto.ServerMsgToolCallStarted:
		updateField = cursorproto.IU_ToolCallStarted
	case cursorproto.ServerMsgToolCallCompleted:
		updateField = cursorproto.IU_ToolCallCompleted
	case cursorproto.ServerMsgPartialToolCall:
		updateField = cursorproto.IU_PartialToolCall
	default:
		return ""
	}
	update, ok := cursorBytesField(interaction, updateField)
	if !ok {
		return ""
	}
	toolCall, ok := cursorBytesField(update, cursorproto.TCU_ToolCall)
	if !ok {
		return ""
	}
	mcpTool, ok := cursorBytesField(toolCall, 15)
	if !ok {
		return ""
	}
	mcpArgs, ok := cursorBytesField(mcpTool, 1)
	if !ok {
		return ""
	}
	if name, ok := cursorStringField(mcpArgs, cursorproto.MCA_ToolName); ok && name != "" {
		return name
	}
	name, _ := cursorStringField(mcpArgs, cursorproto.MCA_Name)
	return name
}

func dispatchCursorExec(
	ctx context.Context,
	stream cursorStreamConn,
	msg *cursorproto.DecodedServerMessage,
	req cursorExecRequest,
	mcpTools []cursorproto.McpToolDef,
) error {
	stopPulse := startCursorExecHeartbeat(ctx, stream, msg.ExecMsgId)
	defer stopPulse()

	const unavailable = "The proxy cannot execute local tools in its own environment. Use an advertised MCP tool instead."
	const notImplemented = "This Cursor exec capability is not implemented by the proxy."

	var response []byte
	switch req.caseNum {
	case cursorproto.ESM_ShellArgs, cursorproto.ESM_ShellStreamArgs:
		response = cursorproto.EncodeExecShellRejected(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), cursorStringFieldOrEmpty(req.args, 2), unavailable)
	case cursorproto.ESM_WriteArgs:
		response = cursorproto.EncodeExecWriteRejected(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), unavailable)
	case cursorproto.ESM_DeleteArgs:
		response = cursorproto.EncodeExecDeleteRejected(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), unavailable)
	case cursorproto.ESM_GrepArgs:
		response = cursorproto.EncodeExecGrepError(msg.ExecMsgId, msg.ExecId, unavailable)
	case cursorproto.ESM_ReadArgs:
		response = cursorproto.EncodeExecReadRejected(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), unavailable)
	case cursorproto.ESM_LsArgs:
		response = cursorproto.EncodeExecLsRejected(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), unavailable)
	case cursorproto.ESM_DiagnosticsArgs:
		response = cursorEncodeExecDiagnosticsError(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), unavailable)
	case cursorproto.ESM_RequestContextArgs:
		response = cursorproto.EncodeExecRequestContextResult(msg.ExecMsgId, msg.ExecId, mcpTools)
	case cursorproto.ESM_BackgroundShellSpawn:
		response = cursorproto.EncodeExecBackgroundShellSpawnRejected(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), cursorStringFieldOrEmpty(req.args, 2), unavailable)
	case cursorproto.ESM_ListMcpResourcesArgs:
		response = cursorproto.EncodeExecListMcpResourcesEmptyResult(msg.ExecMsgId, msg.ExecId)
	case cursorproto.ESM_ReadMcpResourceArgs:
		response = cursorproto.EncodeExecReadMcpResourceNotFound(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 2))
	case cursorproto.ESM_FetchArgs:
		response = cursorproto.EncodeExecFetchError(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), notImplemented)
	case cursorproto.ESM_RecordScreenArgs:
		response = cursorproto.EncodeExecRecordScreenError(msg.ExecMsgId, msg.ExecId, notImplemented)
	case cursorproto.ESM_ComputerUseArgs:
		response = cursorproto.EncodeExecComputerUseError(msg.ExecMsgId, msg.ExecId, notImplemented)
	case cursorproto.ESM_WriteShellStdinArgs:
		response = cursorproto.EncodeExecWriteShellStdinError(msg.ExecMsgId, msg.ExecId, notImplemented)
	case cursorproto.ESM_ExecuteHookArgs:
		request, _ := cursorBytesField(req.args, cursorproto.EHA_Request)
		hookCase := cursorFirstBytesFieldNumber(request)
		if result, ok := cursorproto.EncodeExecHookNeutralResult(msg.ExecMsgId, msg.ExecId, hookCase); ok {
			response = result
		} else {
			response = cursorproto.EncodeExecControlThrow(msg.ExecMsgId, "Unsupported Cursor hook request", "unknown_hook_request")
		}
	case cursorproto.ESM_SubagentArgs:
		response = cursorproto.EncodeExecSubagentError(msg.ExecMsgId, msg.ExecId, "Subagents are not implemented by the proxy")
	case cursorproto.ESM_RedactedReadArgs:
		response = cursorproto.EncodeExecRedactedReadError(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), "Secret redaction is not implemented by the proxy")
	case cursorproto.ESM_ForceBackgroundShellArgs:
		response = cursorproto.EncodeExecForceBackgroundShellNotFound(msg.ExecMsgId, msg.ExecId)
	case cursorproto.ESM_ForceBackgroundSubagentArgs:
		response = cursorproto.EncodeExecForceBackgroundSubagentNotFound(msg.ExecMsgId, msg.ExecId)
	case cursorproto.ESM_McpStateArgs:
		response = cursorproto.EncodeExecMcpStateResult(msg.ExecMsgId, msg.ExecId, mcpTools)
	case cursorproto.ESM_SubagentAwaitArgs:
		response = cursorproto.EncodeExecSubagentAwaitNotFound(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1))
	case cursorproto.ESM_SmartModeClassifierArgs:
		response = cursorproto.EncodeExecSmartModeClassifierError(msg.ExecMsgId, msg.ExecId, "Smart-mode classification is not implemented by the proxy")
	case cursorproto.ESM_CanvasDiagnosticsArgs:
		response = cursorproto.EncodeExecCanvasDiagnosticsError(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), notImplemented)
	case cursorproto.ESM_ShellAllowlistPrecheckArgs:
		response = cursorproto.EncodeExecShellAllowlistPrecheckResult(msg.ExecMsgId, msg.ExecId, false)
	case cursorproto.ESM_McpAllowlistPrecheckArgs:
		response = cursorproto.EncodeExecMcpAllowlistPrecheckResult(msg.ExecMsgId, msg.ExecId, false)
	case cursorproto.ESM_WebFetchAllowlistPrecheckArgs:
		response = cursorproto.EncodeExecWebFetchAllowlistPrecheckResult(msg.ExecMsgId, msg.ExecId, false)
	case cursorproto.ESM_GitDiffRequest:
		response = cursorproto.EncodeExecControlThrow(msg.ExecMsgId, "Git diff is not implemented by the proxy", "exec_variant_unsupported")
	case cursorproto.ESM_PiReadArgs:
		response = cursorproto.EncodeExecPiReadError(msg.ExecMsgId, msg.ExecId, unavailable)
	case cursorproto.ESM_PiBashArgs:
		response = cursorproto.EncodeExecPiBashError(msg.ExecMsgId, msg.ExecId, unavailable)
	case cursorproto.ESM_PiEditArgs:
		response = cursorproto.EncodeExecPiEditError(msg.ExecMsgId, msg.ExecId, unavailable)
	case cursorproto.ESM_PiWriteArgs:
		response = cursorproto.EncodeExecPiWriteError(msg.ExecMsgId, msg.ExecId, unavailable)
	case cursorproto.ESM_PiGrepArgs:
		response = cursorproto.EncodeExecPiGrepError(msg.ExecMsgId, msg.ExecId, unavailable)
	case cursorproto.ESM_PiFindArgs:
		response = cursorproto.EncodeExecPiFindError(msg.ExecMsgId, msg.ExecId, unavailable)
	case cursorproto.ESM_PiLsArgs:
		response = cursorproto.EncodeExecPiLsError(msg.ExecMsgId, msg.ExecId, unavailable)
	case cursorproto.ESM_MiniSweAgentBashArgs:
		response = cursorproto.EncodeExecMiniSweAgentBashRejected(msg.ExecMsgId, msg.ExecId, cursorStringFieldOrEmpty(req.args, 1), cursorStringFieldOrEmpty(req.args, 2), unavailable)
	case cursorproto.ESM_ConversationSearchArgs:
		response = cursorproto.EncodeExecConversationSearchError(msg.ExecMsgId, msg.ExecId, "Conversation search is not implemented by the proxy")
	case cursorproto.ESM_AgentStoreConflictArgs:
		response = cursorproto.EncodeExecAgentStoreConflictError(msg.ExecMsgId, msg.ExecId, "Agent-store conflict handling is not implemented by the proxy")
	default:
		response = cursorproto.EncodeExecControlThrow(msg.ExecMsgId, fmt.Sprintf("No handler for Cursor exec message case %d", req.caseNum), "exec_variant_unsupported")
	}

	if err := cursorWriteClientMessage(stream, response); err != nil {
		return err
	}
	return cursorWriteClientMessage(stream, cursorproto.EncodeExecControlStreamClose(msg.ExecMsgId))
}

func startCursorExecHeartbeat(ctx context.Context, stream cursorStreamConn, execMsgID uint32) func() {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(cursorExecHeartbeatInterval)
		defer timer.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-timer.C:
				if err := cursorWriteClientMessage(stream, cursorproto.EncodeExecControlHeartbeat(execMsgID)); err != nil {
					return
				}
				// Schedule only after the synchronous H2 write above has succeeded.
				timer.Reset(cursorExecHeartbeatInterval)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func cursorWriteClientMessage(stream cursorStreamConn, payload []byte) error {
	if err := stream.Write(cursorproto.FrameConnectMessage(payload, 0)); err != nil {
		return fmt.Errorf("cursor: failed to write server-frame response: %w", err)
	}
	return nil
}

func cursorExecRequestFromPayload(payload []byte) (cursorExecRequest, bool) {
	execMessage, ok := cursorBytesField(payload, cursorproto.ASM_ExecServerMessage)
	if !ok {
		return cursorExecRequest{}, false
	}
	for len(execMessage) > 0 {
		num, typ, tagLen := consumeTag(execMessage)
		if tagLen < 0 {
			return cursorExecRequest{}, false
		}
		execMessage = execMessage[tagLen:]
		if typ == 2 {
			value, valueLen := consumeBytes(execMessage)
			if valueLen < 0 {
				return cursorExecRequest{}, false
			}
			execMessage = execMessage[valueLen:]
			if num != cursorproto.ESM_ExecId && num != 19 {
				return cursorExecRequest{caseNum: num, args: append([]byte(nil), value...)}, true
			}
			continue
		}
		valueLen := consumeFieldValue(num, typ, execMessage)
		if valueLen < 0 {
			return cursorExecRequest{}, false
		}
		execMessage = execMessage[valueLen:]
	}
	return cursorExecRequest{}, false
}

func cursorBytesField(data []byte, target int) ([]byte, bool) {
	for len(data) > 0 {
		num, typ, tagLen := consumeTag(data)
		if tagLen < 0 {
			return nil, false
		}
		data = data[tagLen:]
		if typ == 2 {
			value, valueLen := consumeBytes(data)
			if valueLen < 0 {
				return nil, false
			}
			if num == target {
				return value, true
			}
			data = data[valueLen:]
			continue
		}
		valueLen := consumeFieldValue(num, typ, data)
		if valueLen < 0 {
			return nil, false
		}
		data = data[valueLen:]
	}
	return nil, false
}

func cursorStringField(data []byte, target int) (string, bool) {
	value, ok := cursorBytesField(data, target)
	return string(value), ok
}

func cursorStringFieldOrEmpty(data []byte, target int) string {
	value, _ := cursorStringField(data, target)
	return value
}

func cursorFirstBytesFieldNumber(data []byte) int {
	for len(data) > 0 {
		num, typ, tagLen := consumeTag(data)
		if tagLen < 0 {
			return 0
		}
		data = data[tagLen:]
		valueLen := consumeFieldValue(num, typ, data)
		if valueLen < 0 {
			return 0
		}
		if typ == 2 {
			return num
		}
		data = data[valueLen:]
	}
	return 0
}

// The proto package intentionally exposes no diagnostics-error encoder. Keep
// the missing modern result helper local to the executor as requested.
func cursorEncodeExecDiagnosticsError(execMsgID uint32, execID, path, reason string) []byte {
	var diagnosticsError []byte
	diagnosticsError = cursorAppendString(diagnosticsError, 1, path)
	diagnosticsError = cursorAppendString(diagnosticsError, 2, reason)
	diagnosticsResult := cursorAppendBytes(nil, 2, diagnosticsError)

	var execClient []byte
	execClient = cursorAppendVarintField(execClient, cursorproto.ECM_Id, uint64(execMsgID))
	execClient = cursorAppendString(execClient, cursorproto.ECM_ExecId, execID)
	execClient = cursorAppendBytes(execClient, cursorproto.ECM_DiagnosticsResult, diagnosticsResult)
	return cursorAppendBytes(nil, cursorproto.ACM_ExecClientMessage, execClient)
}

func cursorAppendVarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

func cursorAppendTag(dst []byte, fieldNumber, wireType int) []byte {
	return cursorAppendVarint(dst, uint64(fieldNumber<<3|wireType))
}

func cursorAppendVarintField(dst []byte, fieldNumber int, value uint64) []byte {
	dst = cursorAppendTag(dst, fieldNumber, 0)
	return cursorAppendVarint(dst, value)
}

func cursorAppendBytes(dst []byte, fieldNumber int, value []byte) []byte {
	dst = cursorAppendTag(dst, fieldNumber, 2)
	dst = cursorAppendVarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func cursorAppendString(dst []byte, fieldNumber int, value string) []byte {
	return cursorAppendBytes(dst, fieldNumber, []byte(value))
}

func isCursorResourceExhausted(err error) bool {
	if err == nil {
		return false
	}
	var connectErr *cursorproto.ConnectError
	if errors.As(err, &connectErr) {
		return connectErr.Code == "resource_exhausted"
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "resource_exhausted") || strings.Contains(message, "resource exhausted")
}

// isCursorZeroTokenResourceExhausted reports whether a stream failure is the
// poisoned-conversation signal that makes the wire conversation id eligible for
// rotation, mirroring senpi's isZeroTokenResourceExhausted
// (packages/ai/src/api/cursor-conversation-rotation.ts). senpi gates strictly on
// whether a streamed tokenDelta arrived: checkpoint and billed turnEnded frames
// report context size, not generation progress, so neither clears the gate.
func isCursorZeroTokenResourceExhausted(err error, usage *cursorTokenUsage) bool {
	if !isCursorResourceExhausted(err) {
		return false
	}
	return usage == nil || !usage.sawTokenDelta()
}

// effectiveCursorConversation returns the wire conversation id for a base
// conversation id. A poisoned record (skip) remints a fresh wire id with reset
// counters here — the remint point of senpi's getWireId
// (cursor-conversation-rotation.ts:60-76) — so a poisoned conversation never
// wedges the base id for the process lifetime.
func (e *CursorExecutor) effectiveCursorConversation(baseConversationID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := e.rotations[baseConversationID]
	if rec == nil {
		return baseConversationID
	}
	if !rec.skip {
		return rec.wireID
	}
	rec.wireID = uuid.New().String()
	rec.skip = false
	rec.poisonCount = 0
	// A reminted id is a fresh conversation: it earns its own surface-first
	// pass so compaction runs again before rotation resumes.
	rec.surfaced = false
	return rec.wireID
}

type cursorRotationOutcome int

const (
	// cursorRotationSurface returns the classified error to the client
	// UN-ROTATED: the first zero-token resource_exhausted for a base
	// conversation may be an oversized payload only the client can shrink.
	cursorRotationSurface cursorRotationOutcome = iota
	// cursorRotationRotated retries the Run once on the returned fresh wire id.
	cursorRotationRotated
	// cursorRotationPoisoned surfaces the poisoned-conversation error.
	cursorRotationPoisoned
)

// rotateOnCursorZeroTokenRE mirrors senpi's zero-token resource_exhausted
// catch-block policy (cursor-agent.ts:893-910, cursor-conversation-rotation.ts):
// the first 0-token RE is surfaced un-rotated (surface-first), subsequent ones
// rotate up to cursorMaxConversationRotations, and past the cap the base
// conversation is poisoned (skip) until the next effectiveCursorConversation
// use remints it.
func (e *CursorExecutor) rotateOnCursorZeroTokenRE(baseConversationID, currentConversationID, authID string) (string, cursorRotationOutcome) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := e.rotations[baseConversationID]
	if rec == nil {
		rec = &cursorConversationRotation{wireID: currentConversationID}
		e.rotations[baseConversationID] = rec
	}
	if rec.skip || rec.poisonCount >= cursorMaxConversationRotations {
		rec.skip = true
		rec.wireID = currentConversationID
		return "", cursorRotationPoisoned
	}
	if !rec.surfaced {
		// First 0-token RE for this conversation: surface it so the session
		// layer can compact. If the payload really was oversized, the compacted
		// retry succeeds and no rotation is ever spent.
		rec.surfaced = true
		return "", cursorRotationSurface
	}
	rotated := uuid.New().String()
	rec.wireID = rotated
	rec.poisonCount++
	e.migrateCursorConversationStateLocked(currentConversationID, rotated, authID)
	return rotated, cursorRotationRotated
}

// migrateCursorConversationStateLocked moves the cached checkpoint plus any
// retained MCP session to the replacement wire id. Caller must hold e.mu.
func (e *CursorExecutor) migrateCursorConversationStateLocked(currentConversationID, rotated, authID string) {
	if checkpoint := e.checkpoints[currentConversationID]; checkpoint != nil {
		e.checkpoints[rotated] = checkpoint
		delete(e.checkpoints, currentConversationID)
	}

	oldSuffix := ":" + currentConversationID
	for key, session := range e.sessions {
		if !strings.HasSuffix(key, oldSuffix) || (authID != "" && session.authID != authID) {
			continue
		}
		prefix := strings.TrimSuffix(key, oldSuffix)
		e.sessions[prefix+":"+rotated] = session
		delete(e.sessions, key)
	}
}

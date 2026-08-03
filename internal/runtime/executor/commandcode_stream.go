package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
)

// commandCodeEventKind tags every NDJSON event the command-code@1.6.0
// consumeStream parser recognizes, plus unknown and malformed fallbacks.
type commandCodeEventKind int

const (
	commandCodeEventMalformed commandCodeEventKind = iota
	commandCodeEventUnknown
	commandCodeEventTextDelta
	commandCodeEventReasoningStart
	commandCodeEventReasoningDelta
	commandCodeEventReasoningEnd
	commandCodeEventToolCall
	commandCodeEventToolResult
	commandCodeEventFinishStep
	commandCodeEventFinish
	commandCodeEventError
	commandCodeEventAbort
)

// commandCodeTerminalKind classifies how a CommandCode stream ended.
type commandCodeTerminalKind int

const (
	commandCodeTerminalNone commandCodeTerminalKind = iota
	commandCodeTerminalFinish
	commandCodeTerminalAbort
	commandCodeTerminalError
	commandCodeTerminalTruncation
)

// commandCodeUsage carries token counters from finish-step (incremental) and
// finish (final totalUsage) events. Cache tokens are a subset of the input
// tokens; system prompt tokens are tracked separately and never added.
type commandCodeUsage struct {
	inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, systemPromptTokens int64
}

// commandCodeWireEvent is the strict wire shape of one NDJSON line.
type commandCodeWireEvent struct {
	Type               string                `json:"type"`
	Text               string                `json:"text"`
	ToolCallID         string                `json:"toolCallId"`
	ToolName           string                `json:"toolName"`
	Input              json.RawMessage       `json:"input"`
	Output             json.RawMessage       `json:"output"`
	FinishReason       string                `json:"finishReason"`
	SystemPromptTokens int64                 `json:"systemPromptTokens"`
	Usage              *commandCodeWireUsage `json:"usage"`
	TotalUsage         *commandCodeWireUsage `json:"totalUsage"`
	// Error stays raw: npm 1.6.0 (readStreamErrorEvent) allows the payload
	// to be either a quoted string or an object; a typed field here would
	// fail the whole-line unmarshal on the string form.
	Error              json.RawMessage       `json:"error"`
}

type commandCodeWireUsage struct {
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	InputTokenDetails *struct {
		CacheReadTokens  int64 `json:"cacheReadTokens"`
		CacheWriteTokens int64 `json:"cacheWriteTokens"`
	} `json:"inputTokenDetails"`
}

type commandCodeWireError struct {
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode"`
}

// commandCodeStreamEvent is one strictly decoded, tagged NDJSON event.
type commandCodeStreamEvent struct {
	kind                                     commandCodeEventKind
	text, toolCallID, toolName, finishReason string
	toolInput                                json.RawMessage
	usage                                    commandCodeUsage
	errMessage                               string
	errStatus                                int
	hasUsage                                 bool
}

// commandCodeEventUsage converts wire usage into accumulator usage.
func commandCodeEventUsage(u *commandCodeWireUsage, systemTokens int64) (commandCodeUsage, bool) {
	if u == nil {
		return commandCodeUsage{}, false
	}
	usage := commandCodeUsage{inputTokens: u.InputTokens, outputTokens: u.OutputTokens, systemPromptTokens: systemTokens}
	if d := u.InputTokenDetails; d != nil {
		usage.cacheReadTokens, usage.cacheWriteTokens = d.CacheReadTokens, d.CacheWriteTokens
	}
	return usage, true
}

// decodeCommandCodeStreamEvent strictly decodes one NDJSON line into a tagged
// event. Invalid JSON yields commandCodeEventMalformed (logged at debug level
// and skipped by the accumulator, mirroring the npm consumeStream
// JSON.parse try/catch -> continue); well-formed JSON with an unrecognized
// type yields commandCodeEventUnknown.
func decodeCommandCodeStreamEvent(line []byte) commandCodeStreamEvent {
	var wire commandCodeWireEvent
	if err := json.Unmarshal(line, &wire); err != nil {
		log.Debugf("commandcode: skipping malformed NDJSON line (parse error: %v): %.160s", err, string(line))
		return commandCodeStreamEvent{kind: commandCodeEventMalformed}
	}
	switch wire.Type {
	case "text-delta":
		return commandCodeStreamEvent{kind: commandCodeEventTextDelta, text: wire.Text}
	case "reasoning-start":
		return commandCodeStreamEvent{kind: commandCodeEventReasoningStart}
	case "reasoning-delta":
		return commandCodeStreamEvent{kind: commandCodeEventReasoningDelta}
	case "reasoning-end":
		return commandCodeStreamEvent{kind: commandCodeEventReasoningEnd}
	case "tool-call":
		input := wire.Input
		if len(bytes.TrimSpace(input)) == 0 {
			input = json.RawMessage("{}")
		}
		return commandCodeStreamEvent{kind: commandCodeEventToolCall, toolCallID: wire.ToolCallID, toolName: wire.ToolName, toolInput: input}
	case "tool-result":
		return commandCodeStreamEvent{kind: commandCodeEventToolResult, toolCallID: wire.ToolCallID, toolName: wire.ToolName, text: commandCodeToolResultText(wire.Output)}
	case "finish-step":
		usage, has := commandCodeEventUsage(wire.Usage, 0)
		return commandCodeStreamEvent{kind: commandCodeEventFinishStep, usage: usage, hasUsage: has}
	case "finish":
		usage, has := commandCodeEventUsage(wire.TotalUsage, wire.SystemPromptTokens)
		return commandCodeStreamEvent{kind: commandCodeEventFinish, finishReason: wire.FinishReason, usage: usage, hasUsage: has}
	case "error":
		event := commandCodeStreamEvent{kind: commandCodeEventError, errStatus: http.StatusInternalServerError}
		message, statusCode := decodeCommandCodeWireError(wire.Error)
		event.errMessage = message
		if statusCode > 0 {
			event.errStatus = statusCode
		}
		return event
	case "abort":
		return commandCodeStreamEvent{kind: commandCodeEventAbort}
	default:
		return commandCodeStreamEvent{kind: commandCodeEventUnknown}
	}
}

// decodeCommandCodeWireError unwraps the wire error payload, mirroring npm
// 1.6.0 readStreamErrorEvent: a non-empty quoted string payload becomes the
// message with no explicit status; an object payload contributes message and
// statusCode; anything missing or unusable falls back to the generic
// "Stream error" message with no explicit status (the caller defaults the
// status to 500, as npm does via `e.statusCode ?? 500`).
func decodeCommandCodeWireError(raw json.RawMessage) (message string, statusCode int) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err == nil && text != "" {
			return text, 0
		}
		return "Stream error", 0
	}
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		var obj commandCodeWireError
		if err := json.Unmarshal(trimmed, &obj); err == nil {
			if obj.Message != "" {
				return obj.Message, obj.StatusCode
			}
			return "Stream error", obj.StatusCode
		}
	}
	return "Stream error", 0
}

// commandCodeToolResultText renders a provider-executed tool-result output as
// visible text. String outputs are unquoted; structured outputs keep their
// compact JSON form.
func commandCodeToolResultText(output json.RawMessage) string {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return text
	}
	return string(trimmed)
}

// commandCodeToolCall is an accumulated tool call with its original upstream
// ID and parsed JSON input.
type commandCodeToolCall struct {
	id, name  string
	arguments json.RawMessage
}

// commandCodeStreamAccumulator is the shared NDJSON stream state machine used
// by both Execute and ExecuteStream. Hidden reasoning is tracked only as
// internal lifecycle state and never exposed downstream.
type commandCodeStreamAccumulator struct {
	text                                   strings.Builder
	toolCalls                              []commandCodeToolCall
	incremental, final                     commandCodeUsage
	rawFinishReason, stopReason            string
	err                                    error
	terminal                               commandCodeTerminalKind
	reasoningOpen, sawKnownEvent, hasFinal bool
	// rawRetained holds a bounded prefix of the raw NDJSON body, kept only
	// while no known event has been seen, so a legacy single-envelope JSON
	// body can still be recovered. Once a known event arrives the body can
	// never be a legacy envelope, so the buffer is released and stays empty
	// for the rest of the stream (memory stays flat on very large streams).
	rawRetained []byte
}

// commandCodeMaxRetainedRawBytes bounds how many raw NDJSON bytes the
// accumulator retains for legacy envelope recovery. A realistic legacy
// envelope is a single small JSON body; the cap only guards against
// pathological streams of unknown/malformed lines.
const commandCodeMaxRetainedRawBytes = 8 << 20 // 8 MiB

func newCommandCodeStreamAccumulator() *commandCodeStreamAccumulator {
	return &commandCodeStreamAccumulator{stopReason: "stop"}
}

// feed folds one decoded event into the accumulator state. Events arriving
// after a terminal event are ignored. commandCodeEventUnknown and
// commandCodeEventMalformed are ignored: a body with no known events at all
// falls back to legacy envelope recovery in Execute before truncation is
// declared.
func (a *commandCodeStreamAccumulator) feed(event commandCodeStreamEvent) {
	if a.terminal != commandCodeTerminalNone {
		return
	}
	switch event.kind {
	case commandCodeEventTextDelta, commandCodeEventToolResult:
		a.sawKnownEvent = true
		a.text.WriteString(event.text)
	case commandCodeEventReasoningStart:
		a.sawKnownEvent, a.reasoningOpen = true, true
	case commandCodeEventReasoningDelta:
		// Hidden reasoning stays internal; it never reaches any output path.
		a.sawKnownEvent = true
	case commandCodeEventReasoningEnd:
		a.sawKnownEvent, a.reasoningOpen = true, false
	case commandCodeEventToolCall:
		a.sawKnownEvent = true
		a.toolCalls = append(a.toolCalls, commandCodeToolCall{id: event.toolCallID, name: event.toolName, arguments: event.toolInput})
	case commandCodeEventFinishStep:
		a.sawKnownEvent = true
		if event.hasUsage {
			a.incremental.inputTokens += event.usage.inputTokens
			a.incremental.outputTokens += event.usage.outputTokens
			a.incremental.cacheReadTokens += event.usage.cacheReadTokens
			a.incremental.cacheWriteTokens += event.usage.cacheWriteTokens
		}
	case commandCodeEventFinish:
		a.sawKnownEvent = true
		a.terminal = commandCodeTerminalFinish
		a.rawFinishReason = event.finishReason
		a.stopReason = normalizeCommandCodeStopReason(event.finishReason)
		if event.hasUsage {
			a.final, a.hasFinal = event.usage, true
		}
	case commandCodeEventError:
		a.sawKnownEvent = true
		a.terminal = commandCodeTerminalError
		a.err = statusErr{code: event.errStatus, msg: fmt.Sprintf("commandcode: upstream stream error: %s", event.errMessage)}
	case commandCodeEventAbort:
		a.sawKnownEvent = true
		a.terminal = commandCodeTerminalAbort
		a.stopReason = "stop"
	case commandCodeEventMalformed:
		// npm 1.6.0 consumeStream skips lines whose JSON.parse fails
		// (try/catch -> continue) instead of aborting the stream. The line is
		// already logged at debug level by decodeCommandCodeStreamEvent; a
		// body with no recognized events at all still falls back to legacy
		// envelope recovery, and EOF without finish/abort is still classified
		// as truncation by finishEOF, so skipping loses no safety guarantee.
	}
}

// feedLine decodes one raw NDJSON line, folds it into the accumulator, and
// manages the bounded raw retention used for legacy envelope recovery. Raw
// bytes are retained only while no known event has been seen (clear/swap on
// the first known event) and never beyond commandCodeMaxRetainedRawBytes, so
// retention does not grow unbounded with very large streams. Truncation
// detection does not depend on this buffer: finishEOF classifies EOF from
// accumulator state alone.
func (a *commandCodeStreamAccumulator) feedLine(line []byte) {
	a.feed(decodeCommandCodeStreamEvent(line))
	if a.sawKnownEvent {
		a.rawRetained = nil
		return
	}
	if len(a.rawRetained)+len(line)+1 > commandCodeMaxRetainedRawBytes {
		return
	}
	if len(a.rawRetained) > 0 {
		a.rawRetained = append(a.rawRetained, '\n')
	}
	a.rawRetained = append(a.rawRetained, line...)
}

// retainedRawBody returns the bounded raw NDJSON bytes retained so far for
// legacy envelope recovery, or nil once a known event released the buffer.
func (a *commandCodeStreamAccumulator) retainedRawBody() []byte {
	return a.rawRetained
}

// usage resolves the effective usage: the finish event's final totalUsage
// overrides the incremental finish-step usage when both exist.
func (a *commandCodeStreamAccumulator) usage() commandCodeUsage {
	if a.hasFinal {
		return a.final
	}
	return a.incremental
}

// finishEOF classifies EOF without a finish or abort event as a retryable 502
// truncation instead of a false-success partial response. On an already
// terminated stream it returns the terminal error, if any.
func (a *commandCodeStreamAccumulator) finishEOF() error {
	if a.terminal != commandCodeTerminalNone {
		return a.err
	}
	a.terminal = commandCodeTerminalTruncation
	a.err = statusErr{code: http.StatusBadGateway, msg: "commandcode: stream truncated before a finish or abort event"}
	return a.err
}

// adoptLegacyText marks a non-NDJSON legacy JSON envelope body as a finished
// stream carrying the recovered visible text.
func (a *commandCodeStreamAccumulator) adoptLegacyText(text string) {
	a.text.WriteString(text)
	a.terminal = commandCodeTerminalFinish
	a.stopReason = "stop"
}

// openAIToolCalls renders accumulated tool calls in OpenAI message shape.
func (a *commandCodeStreamAccumulator) openAIToolCalls() []map[string]any {
	calls := make([]map[string]any, 0, len(a.toolCalls))
	for index, call := range a.toolCalls {
		calls = append(calls, map[string]any{
			"index": index, "id": call.id, "type": "function",
			"function": map[string]any{"name": call.name, "arguments": string(call.arguments)},
		})
	}
	return calls
}

// normalizeCommandCodeStopReason maps npm finish reasons to downstream stop
// reasons: tool-calls -> tool_calls, length -> max_tokens, everything else
// (stop, end_turn, empty, unknown) -> stop. A raw pause_turn stays available
// on the accumulator for continuation logic but never surfaces as a final
// stop reason.
func normalizeCommandCodeStopReason(raw string) string {
	switch raw {
	case "tool-calls":
		return "tool_calls"
	case "length":
		return "max_tokens"
	default:
		return "stop"
	}
}

// commandCodeOpenAIUsage renders usage in OpenAI shape. Cache tokens are a
// subset of the prompt tokens (details, not additive) and system prompt
// tokens never corrupt the totals.
func commandCodeOpenAIUsage(usage commandCodeUsage) map[string]any {
	out := map[string]any{
		"prompt_tokens": usage.inputTokens, "completion_tokens": usage.outputTokens,
		"total_tokens": usage.inputTokens + usage.outputTokens,
	}
	if usage.cacheReadTokens > 0 || usage.cacheWriteTokens > 0 {
		out["prompt_tokens_details"] = map[string]any{
			"cached_tokens": usage.cacheReadTokens, "cached_creation_tokens": usage.cacheWriteTokens,
		}
	}
	return out
}

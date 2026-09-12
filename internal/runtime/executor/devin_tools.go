package executor

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Wire layout confirmed against the published JavaScript implementation of the
// same protocol (npm ai-sdk-devin, dist/chat.js), whose field numbers are
// documented from a capture of the Windsurf language server.
const (
	// Request fields.
	devinReqToolsField = 10

	// ChatToolDefinition fields.
	devinToolDefNameField   = 1
	devinToolDefDescField   = 2
	devinToolDefParamsField = 3

	// ChatToolCall / ToolCallDelta fields.
	devinToolCallIDField   = 1
	devinToolCallNameField = 2
	devinToolCallArgsField = 3

	// Response fields.
	devinFinishReasonField = 5
	devinToolCallField     = 6
	devinUsageField        = 7

	// Cost accounting fields on the response.
	devinCreditCostField      = 14
	devinCommittedCreditField = 18
	devinCommittedACUField    = 22
	devinSignatureField       = 10
	devinSignatureTypeField   = 21

	// ModelUsageStats submessage fields.
	devinUsageInputTokensField  = 2
	devinUsageOutputTokensField = 3
	devinUsageCacheWriteField   = 4
	devinUsageCacheReadField    = 5

	// StopReason enum values that are not a plain stop.
	devinStopIncomplete    = 1
	devinStopMaxTokens     = 3
	devinStopFunctionCall  = 10
	devinStopContentFilter = 11
	devinStopError         = 13

	// Devin truncates very long tool descriptions upstream.
	devinMaxToolDescLen = 1024
)

// devinToolDef is one tool exposed to the model.
type devinToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// devinToolCall is a tool invocation requested by the model.
type devinToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// devinUsage carries token accounting reported by the upstream.
type devinUsage struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CacheWriteTokens int
	CreditCost       int
	CommittedCredit  int
	CommittedACU     float64
	ReasoningTokens  int
}

// Total reports the combined token count.
func (u devinUsage) Total() int { return u.PromptTokens + u.CompletionTokens }

// Empty reports whether the upstream sent no usage numbers at all.
func (u devinUsage) Empty() bool {
	return u.PromptTokens == 0 && u.CompletionTokens == 0 && u.CachedTokens == 0 && u.ReasoningTokens == 0
}

// devinExtractTools reads OpenAI-style tool definitions from a request payload.
func devinExtractTools(payload []byte) []devinToolDef {
	var doc struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil
	}
	out := make([]devinToolDef, 0, len(doc.Tools))
	for _, t := range doc.Tools {
		name := strings.TrimSpace(t.Function.Name)
		if name == "" {
			continue
		}
		out = append(out, devinToolDef{
			Name:        name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	return out
}

// devinEncodeToolDef encodes one ChatToolDefinition submessage.
// devinToolDescSuffix marks a description the cloud limit forced us to cut.
const devinToolDescSuffix = "\n…(truncated for cloud)"

// devinTruncateToolDesc keeps a tool description under the cloud limit without
// splitting a multi-byte rune.
//
// Slicing by byte cut a Korean description mid-rune and left a dangling 0xED in
// the protobuf string field. Protobuf string fields must be valid UTF-8, so the
// backend rejected the whole request with invalid_argument and no further
// detail - deterministically, for any client whose tool descriptions are not
// pure ASCII, and only once such a tool was in the manifest.
func devinTruncateToolDesc(desc string) string {
	if len(desc) <= devinMaxToolDescLen {
		return desc
	}
	limit := devinMaxToolDescLen - len(devinToolDescSuffix)
	if limit <= 0 {
		return devinToolDescSuffix
	}
	// Walk back to the last rune boundary at or before the limit so the cut
	// never lands inside a multi-byte sequence.
	cut := 0
	for idx := range desc {
		if idx > limit {
			break
		}
		cut = idx
	}
	return desc[:cut] + devinToolDescSuffix
}

func devinEncodeToolDef(t devinToolDef) []byte {
	desc := devinTruncateToolDesc(t.Description)
	params := "{}"
	if len(t.Parameters) > 0 {
		params = string(t.Parameters)
	}
	var msg []byte
	msg = append(msg, devinEncodeField(nil, devinToolDefNameField, 2, devinEncodeString(t.Name))...)
	msg = append(msg, devinEncodeField(nil, devinToolDefDescField, 2, devinEncodeString(desc))...)
	msg = append(msg, devinEncodeField(nil, devinToolDefParamsField, 2, devinEncodeString(params))...)
	return msg
}

// devinFinishReason maps a StopReason enum value to the OpenAI finish_reason.
func devinFinishReason(stop uint64) string {
	switch stop {
	case devinStopFunctionCall:
		return "tool_calls"
	case devinStopContentFilter:
		return "content_filter"
	case devinStopError:
		return "error"
	case devinStopIncomplete, devinStopMaxTokens:
		return "length"
	default:
		return "stop"
	}
}

// devinScanFields walks top-level protobuf fields, invoking fn per field.
// Returning false from fn stops the walk.
func devinScanFields(buf []byte, fn func(num int, wire int, varint uint64, data []byte) bool) {
	pos := 0
	for pos < len(buf) {
		key, n := binary.Uvarint(buf[pos:])
		if n <= 0 {
			return
		}
		pos += n
		num, wire := int(key>>3), int(key&7)
		switch wire {
		case 0:
			v, vn := binary.Uvarint(buf[pos:])
			if vn <= 0 {
				return
			}
			pos += vn
			if !fn(num, wire, v, nil) {
				return
			}
		case 1:
			if pos+8 > len(buf) {
				return
			}
			raw := buf[pos : pos+8]
			pos += 8
			if !fn(num, wire, 0, raw) {
				return
			}
		case 2:
			ln, ln2 := binary.Uvarint(buf[pos:])
			if ln2 <= 0 || pos+ln2+int(ln) > len(buf) {
				return
			}
			raw := buf[pos+ln2 : pos+ln2+int(ln)]
			pos += ln2 + int(ln)
			if !fn(num, wire, 0, raw) {
				return
			}
		case 5:
			if pos+4 > len(buf) {
				return
			}
			raw := buf[pos : pos+4]
			pos += 4
			if !fn(num, wire, 0, raw) {
				return
			}
		default:
			return
		}
	}
}

// devinExtractToolCallDelta reads a ToolCallDelta from one response frame.
func devinExtractToolCallDelta(frame []byte) (devinToolCall, bool) {
	var call devinToolCall
	found := false
	devinScanFields(frame, func(num, wire int, _ uint64, data []byte) bool {
		if num != devinToolCallField || wire != 2 {
			return true
		}
		found = true
		devinScanFields(data, func(sn, sw int, _ uint64, sd []byte) bool {
			if sw != 2 {
				return true
			}
			switch sn {
			case devinToolCallIDField:
				call.ID = string(sd)
			case devinToolCallNameField:
				call.Name = string(sd)
			case devinToolCallArgsField:
				call.Arguments += string(sd)
			}
			return true
		})
		return true
	})
	return call, found
}

// devinExtractFinishReason reads the StopReason enum from a response frame.
func devinExtractFinishReason(frame []byte) (string, bool) {
	reason := ""
	ok := false
	devinScanFields(frame, func(num, wire int, v uint64, _ []byte) bool {
		if num == devinFinishReasonField && wire == 0 {
			reason = devinFinishReason(v)
			ok = true
			return false
		}
		return true
	})
	return reason, ok
}

// devinExtractUsage reads ModelUsageStats from a response frame.
//
// Field numbers follow the published Cascade schema: the response carries
// usage on field 7, whose submessage holds uint64 token counts. An earlier
// implementation read field 28 (response_dimension_groups) and parsed
// float32 metric pairs, which happened to yield numbers but is not the
// documented location.
func devinExtractUsage(frame []byte) (devinUsage, bool) {
	var usage devinUsage
	found := false
	devinScanFields(frame, func(num, wire int, _ uint64, data []byte) bool {
		if num != devinUsageField || wire != 2 {
			return true
		}
		devinScanFields(data, func(fn, fw int, v uint64, _ []byte) bool {
			if fw != 0 {
				return true
			}
			switch fn {
			case devinUsageInputTokensField:
				usage.PromptTokens = int(v)
				found = true
			case devinUsageOutputTokensField:
				usage.CompletionTokens = int(v)
				found = true
			case devinUsageCacheWriteField:
				usage.CacheWriteTokens = int(v)
				found = true
			case devinUsageCacheReadField:
				usage.CachedTokens = int(v)
				found = true
			}
			return true
		})
		return true
	})
	return usage, found
}

// devinMergeToolCall accumulates streamed ToolCallDelta fragments. Arguments
// arrive in pieces, so an existing entry is appended to rather than replaced.
func devinMergeToolCall(calls *[]devinToolCall, byID map[string]int, in devinToolCall) {
	key := in.ID
	if key == "" && len(*calls) > 0 {
		// A delta without an id continues the most recent call.
		last := len(*calls) - 1
		(*calls)[last].Arguments += in.Arguments
		return
	}
	if idx, ok := byID[key]; ok {
		if in.Name != "" {
			(*calls)[idx].Name = in.Name
		}
		(*calls)[idx].Arguments += in.Arguments
		return
	}
	byID[key] = len(*calls)
	*calls = append(*calls, in)
}

// devinToolCallsJSON renders accumulated tool calls as an OpenAI tool_calls array.
func devinToolCallsJSON(calls []devinToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, c := range calls {
		if i > 0 {
			sb.WriteByte(',')
		}
		args := c.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		id := devinNormalizeToolCallID(c.ID, i)
		sb.WriteString(fmt.Sprintf(
			`{"id":%s,"type":"function","function":{"name":%s,"arguments":%s}}`,
			mustMarshalDevinJSON(id), mustMarshalDevinJSON(c.Name), mustMarshalDevinJSON(args)))
	}
	sb.WriteByte(']')
	return sb.String()
}

// devinUsageJSON renders the OpenAI usage object, or an empty object when the
// upstream reported nothing.
func devinUsageJSON(u devinUsage) string {
	if u.Empty() {
		return "{}"
	}
	extra := ""
	if u.CachedTokens > 0 {
		extra = fmt.Sprintf(`,"prompt_tokens_details":{"cached_tokens":%d}`, u.CachedTokens)
	}
	if u.CreditCost > 0 || u.CommittedCredit > 0 || u.CommittedACU > 0 {
		extra += fmt.Sprintf(`,"cost":{"credit":%d,"committed_credit":%d,"committed_acu":%g}`, u.CreditCost, u.CommittedCredit, u.CommittedACU)
	}
	if u.ReasoningTokens > 0 {
		extra += fmt.Sprintf(`,"completion_tokens_details":{"reasoning_tokens":%d}`, u.ReasoningTokens)
	}
	return fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d%s}`,
		u.PromptTokens, u.CompletionTokens, u.Total(), extra)
}

// devinBuildCompletionPayload renders the non-streaming chat.completion body.
func devinBuildCompletionPayload(model, content, reasoning string, calls []devinToolCall, finish string, usage devinUsage) []byte {
	msg := fmt.Sprintf(`{"role":"assistant","content":%s`, mustMarshalDevinJSON(content))
	if reasoning != "" {
		msg += `,"reasoning_content":` + mustMarshalDevinJSON(reasoning)
	}
	if tc := devinToolCallsJSON(calls); tc != "" {
		msg += `,"tool_calls":` + tc
	}
	msg += "}"
	return []byte(fmt.Sprintf(
		`{"id":"chatcmpl-devin","object":"chat.completion","created":0,"model":%s,"choices":[{"index":0,"message":%s,"finish_reason":%s}],"usage":%s}`,
		marshalDevinJSONStringOrEmpty(model), msg, mustMarshalDevinJSON(finish), devinUsageJSON(usage)))
}

// devinToolCallChunk renders the SSE chunk announcing accumulated tool calls.
func devinToolCallChunk(model string, calls []devinToolCall) []byte {
	tc := devinToolCallsJSON(calls)
	if tc == "" {
		return nil
	}
	return []byte(fmt.Sprintf(
		`data: {"id":"chatcmpl-devin","object":"chat.completion.chunk","created":0,"model":%s,"choices":[{"index":0,"delta":{"tool_calls":%s},"finish_reason":null}]}`+"\n\n",
		marshalDevinJSONStringOrEmpty(model), tc))
}

// devinFinishChunk renders the terminating chunk carrying finish_reason and,
// when the upstream reported it, the usage block.
func devinFinishChunk(model, finish string, usage devinUsage) []byte {
	usagePart := ""
	if !usage.Empty() {
		usagePart = `,"usage":` + devinUsageJSON(usage)
	}
	return []byte(fmt.Sprintf(
		`data: {"id":"chatcmpl-devin","object":"chat.completion.chunk","created":0,"model":%s,"choices":[{"index":0,"delta":{},"finish_reason":%s}]%s}`+"\n\n",
		marshalDevinJSONStringOrEmpty(model), mustMarshalDevinJSON(finish), usagePart))
}

// devinNormalizeToolCallID makes a tool call id conform to the OpenAI
// convention. Devin returns bare ids such as "web_search_0", but the
// Responses API expects a call_ prefixed id; clients that validate the
// function_call item reject the bare form and never see the turn complete.
func devinNormalizeToolCallID(id string, index int) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Sprintf("call_devin_%d", index)
	}
	if strings.HasPrefix(id, "call_") {
		return id
	}
	return "call_" + id
}

// devinExtractSignature pulls the thinking signature (f10) and its type (f21).
func devinExtractSignature(frame []byte) (sig, sigType string) {
	devinScanFields(frame, func(num, wire int, _ uint64, data []byte) bool {
		if wire != 2 {
			return true
		}
		switch num {
		case devinSignatureField:
			sig += string(data)
		case devinSignatureTypeField:
			sigType = string(data)
		}
		return true
	})
	return sig, sigType
}

// devinExtractCost reads credit and ACU accounting from a response frame.
func devinExtractCost(frame []byte, usage *devinUsage) bool {
	found := false
	devinScanFields(frame, func(num, wire int, v uint64, data []byte) bool {
		switch {
		case num == devinCreditCostField && wire == 0:
			usage.CreditCost = int(v)
			found = true
		case num == devinCommittedCreditField && wire == 0:
			usage.CommittedCredit = int(v)
			found = true
		case num == devinCommittedACUField && wire == 1 && len(data) == 8:
			usage.CommittedACU = math.Float64frombits(binary.LittleEndian.Uint64(data))
			found = true
		}
		return true
	})
	return found
}

// devinPartialJSONObject returns the longest prefix of a streaming JSON
// object that parses, so tool arguments can be surfaced before the stream
// finishes. It returns false while nothing parses yet.
func devinPartialJSONObject(sofar string) (string, bool) {
	trimmed := strings.TrimSpace(sofar)
	if trimmed == "" {
		return "", false
	}
	var probe any
	if json.Unmarshal([]byte(trimmed), &probe) == nil {
		return trimmed, true
	}
	// Close any still-open braces/brackets so a partial object parses.
	depthCurly, depthSquare, inStr, esc := 0, 0, false, false
	for _, r := range trimmed {
		switch {
		case esc:
			esc = false
		case r == 92 && inStr:
			esc = true
		case r == 34:
			inStr = !inStr
		case inStr:
		case r == 123:
			depthCurly++
		case r == 125:
			depthCurly--
		case r == 91:
			depthSquare++
		case r == 93:
			depthSquare--
		}
	}
	if inStr || depthCurly < 0 || depthSquare < 0 {
		return "", false
	}
	candidate := trimmed
	for i := 0; i < depthSquare; i++ {
		candidate += "]"
	}
	for i := 0; i < depthCurly; i++ {
		candidate += "}"
	}
	if json.Unmarshal([]byte(candidate), &probe) == nil {
		return candidate, true
	}
	return "", false
}

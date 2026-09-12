package executor

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// Remaining wire fields of the Devin GetChatMessage protocol, confirmed
// against the published JavaScript implementation (npm ai-sdk-devin).
const (
	devinReqMetadataField      = 1
	devinReqSystemField        = 2
	devinReqPromptField        = 3
	devinReqTypeField          = 7
	devinReqConfigField        = 8
	devinReqCascadeField       = 16
	devinReqPromptIDField      = 17
	devinReqParallelToolsField = 11
	devinReqToolChoiceField    = 12
	devinReqCacheOptionsField  = 13
	devinReqExecutionIDField   = 22
	devinReqPlannerModeField   = 20
	devinReqModelField         = 21

	// request_type enum: 5 = CASCADE.
	devinRequestTypeCascade = 5

	// devinMaxNewlines mirrors the native client cap.
	devinMaxNewlines = 200

	// CacheControlType.EPHEMERAL
	devinCacheControlEphemeral = 1
)

// Metadata submessage (request f1).
const (
	devinMetaClientField      = 1
	devinMetaVersionField     = 2
	devinMetaAPIKeyField      = 3
	devinMetaLocaleField      = 4
	devinMetaOSField          = 5
	devinMetaClientVerField   = 7
	devinMetaRequestIDField   = 9
	devinMetaSessionIDField   = 10
	devinMetaClientKindField  = 12
	devinMetaTimestampField   = 16
	devinMetaUserJWTField     = 21
	devinMetaTriggerIDField   = 25
	devinMetaUnsetField       = 26
	devinMetaClientKind2Field = 28
	devinMetaFingerprintField = 31
)

// ChatMessagePrompt submessage (request f3).
const (
	devinPromptSessionField       = 1
	devinPromptSourceField        = 2
	devinPromptTextField          = 3
	devinPromptNumTokensField     = 4
	devinPromptSafeTelemetryField = 5
	devinPromptToolCallsField     = 6
	devinPromptToolCallIDField    = 7
	devinPromptToolErrorField     = 9
	devinPromptThinkingField      = 11
	devinPromptSignatureField     = 12
	devinPromptSigTypeField       = 18
)

// Message source enum. Cognition rejects source=3, so a system message is
// carried as a user turn rather than its own source.
const (
	devinSourceUser      = 1
	devinSourceAssistant = 2
	devinSourceTool      = 4
)

// devinCompletionConfig carries sampling parameters (request f8).
type devinCompletionConfig struct {
	MaxInputTokens  int
	MaxOutputTokens int
	Temperature     float64
	TopP            float64
	TopK            int
	StopPatterns    []string
}

// devinDefaultCompletionConfig mirrors the reference client defaults.
func devinDefaultCompletionConfig() devinCompletionConfig {
	return devinCompletionConfig{
		MaxInputTokens:  64000,
		MaxOutputTokens: 64000,
		Temperature:     0.4,
		TopP:            1,
		TopK:            50,
		StopPatterns:    devinDefaultStopPatterns(),
	}
}

// devinDefaultStopPatterns are the turn-boundary control tokens the native
// client always sends.
func devinDefaultStopPatterns() []string {
	return []string{"<|user|>", "<|bot|>", "<|context_request|>", "<|endoftext|>", "<|end_of_turn|>"}
}

// devinExtractCompletionConfig reads sampling parameters from an OpenAI-style
// request. Absent values keep the reference defaults.
func devinExtractCompletionConfig(payload []byte) devinCompletionConfig {
	cfg := devinDefaultCompletionConfig()
	if len(payload) == 0 {
		return cfg
	}
	var doc struct {
		Temperature *float64 `json:"temperature"`
		TopP        *float64 `json:"top_p"`
		TopK        *int     `json:"top_k"`
		MaxTokens   *int     `json:"max_tokens"`
		MaxCompTok  *int     `json:"max_completion_tokens"`
		Stop        any      `json:"stop"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return cfg
	}
	if doc.Temperature != nil {
		cfg.Temperature = *doc.Temperature
	}
	if doc.TopP != nil {
		cfg.TopP = *doc.TopP
	}
	if doc.TopK != nil {
		cfg.TopK = *doc.TopK
	}
	if doc.MaxCompTok != nil && *doc.MaxCompTok > 0 {
		cfg.MaxOutputTokens = *doc.MaxCompTok
	} else if doc.MaxTokens != nil && *doc.MaxTokens > 0 {
		cfg.MaxOutputTokens = *doc.MaxTokens
	}
	// User stop sequences extend the control tokens rather than replacing them.
	if extra := devinStopSequences(doc.Stop); len(extra) > 0 {
		cfg.StopPatterns = append(devinDefaultStopPatterns(), extra...)
	}
	return cfg
}

// devinEncodeDouble encodes a fixed64 double field.
func devinEncodeDouble(num int, v float64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
	out := devinEncodeVarint(nil, uint64(num<<3|1))
	return append(out, b[:]...)
}

// devinEncodeSubMessage wraps a submessage payload in a length-delimited field.
func devinEncodeSubMessage(num int, body []byte) []byte {
	return devinEncodeField(nil, num, 2, append(devinEncodeVarint(nil, uint64(len(body))), body...))
}

// devinEncodeCompletionConfig builds the completion_configuration submessage.
func devinEncodeCompletionConfig(cfg devinCompletionConfig) []byte {
	// CompletionConfiguration mirrors the native client: num_completions=1,
	// max_tokens=2, max_newlines=3, temperature=5, first_temperature=6,
	// top_k=7, top_p=8, stop_patterns=9, fim_eot_prob_threshold=11.
	var msg []byte
	msg = append(msg, devinEncodeField(nil, 1, 0, devinEncodeVarint(nil, 1))...)
	msg = append(msg, devinEncodeField(nil, 2, 0, devinEncodeVarint(nil, uint64(cfg.MaxOutputTokens)))...)
	msg = append(msg, devinEncodeField(nil, 3, 0, devinEncodeVarint(nil, devinMaxNewlines))...)
	msg = append(msg, devinEncodeDouble(5, cfg.Temperature)...)
	msg = append(msg, devinEncodeDouble(6, cfg.Temperature)...)
	msg = append(msg, devinEncodeField(nil, 7, 0, devinEncodeVarint(nil, uint64(cfg.TopK)))...)
	msg = append(msg, devinEncodeDouble(8, cfg.TopP)...)
	// Stop patterns keep the model from emitting turn-boundary control tokens.
	for _, pattern := range cfg.StopPatterns {
		msg = append(msg, devinEncodeField(nil, 9, 2, devinEncodeString(pattern))...)
	}
	msg = append(msg, devinEncodeDouble(11, 1.0)...)
	return msg
}

// devinTimestampBody encodes a protobuf Timestamp submessage.
func devinTimestampBody(t time.Time) []byte {
	var msg []byte
	msg = append(msg, devinEncodeField(nil, 1, 0, devinEncodeVarint(nil, uint64(t.Unix())))...)
	msg = append(msg, devinEncodeField(nil, 2, 0, devinEncodeVarint(nil, uint64(t.Nanosecond())))...)
	return msg
}

// devinSourceForRole maps an OpenAI role to the Devin message source enum.
func devinSourceForRole(role string) int {
	switch role {
	case "assistant":
		return devinSourceAssistant
	case "tool", "function":
		return devinSourceTool
	default:
		return devinSourceUser
	}
}

// devinEstimateTokens mirrors the reference client heuristic.
func devinEstimateTokens(text string) uint64 {
	n := len(text) / 4
	if n < 1 {
		return 1
	}
	return uint64(n)
}

// devinEncodeMessagePrompt builds one ChatMessagePrompt, including any tool
// linkage so a tool-call round trip can continue the agent loop.
func devinEncodeMessagePrompt(sessionUUID string, m devinMessage) []byte {
	var msg []byte
	if sessionUUID != "" {
		msg = append(msg, devinEncodeField(nil, devinPromptSessionField, 2, devinEncodeString(sessionUUID))...)
	}
	msg = append(msg, devinEncodeField(nil, devinPromptSourceField, 0, devinEncodeVarint(nil, uint64(devinSourceForRole(m.role))))...)
	msg = append(msg, devinEncodeField(nil, devinPromptTextField, 2, devinEncodeString(m.content))...)
	msg = append(msg, devinEncodeField(nil, devinPromptNumTokensField, 0, devinEncodeVarint(nil, devinEstimateTokens(m.content)))...)
	msg = append(msg, devinEncodeField(nil, devinPromptSafeTelemetryField, 0, devinEncodeVarint(nil, 1))...)
	if m.toolCallID != "" {
		msg = append(msg, devinEncodeField(nil, devinPromptToolCallIDField, 2, devinEncodeString(m.toolCallID))...)
	}
	// Reasoning models need their own thinking echoed back so the chain
	// survives across turns instead of restarting every request.
	if m.thinking != "" {
		msg = append(msg, devinEncodeField(nil, devinPromptThinkingField, 2, devinEncodeString(m.thinking))...)
	}
	if m.signature != "" {
		msg = append(msg, devinEncodeField(nil, devinPromptSignatureField, 2, devinEncodeString(m.signature))...)
	}
	if m.signatureType != "" {
		msg = append(msg, devinEncodeField(nil, devinPromptSigTypeField, 2, devinEncodeString(m.signatureType))...)
	}
	if m.toolIsError {
		msg = append(msg, devinEncodeField(nil, devinPromptToolErrorField, 0, devinEncodeVarint(nil, 1))...)
	}
	for _, tc := range m.toolCalls {
		var call []byte
		call = append(call, devinEncodeField(nil, devinToolCallIDField, 2, devinEncodeString(tc.ID))...)
		call = append(call, devinEncodeField(nil, devinToolCallNameField, 2, devinEncodeString(tc.Name))...)
		call = append(call, devinEncodeField(nil, devinToolCallArgsField, 2, devinEncodeString(tc.Arguments))...)
		msg = append(msg, devinEncodeSubMessage(devinPromptToolCallsField, call)...)
	}
	return msg
}

// devinChatSpec is the full input to a GetChatMessage request.
type devinChatSpec struct {
	Token                string
	ModelUID             string
	System               string
	Messages             []devinMessage
	SessionUUID          string
	CascadeID            string
	PromptID             string
	TriggerID            string
	RequestID            uint64
	Fingerprint          string
	UserJWT              string
	Tools                []devinToolDef
	AssignmentJWT        string
	ExecutionID          string
	DisableParallelTools bool
	Config               devinCompletionConfig
	Now                  time.Time
}

// devinBuildMetadata builds the request metadata submessage.
func devinBuildMetadata(spec devinChatSpec) []byte {
	var msg []byte
	msg = append(msg, devinEncodeField(nil, devinMetaClientField, 2, devinEncodeString(devinClientName))...)
	msg = append(msg, devinEncodeField(nil, devinMetaVersionField, 2, devinEncodeString(devinClientVersion))...)
	msg = append(msg, devinEncodeField(nil, devinMetaAPIKeyField, 2, devinEncodeString(spec.Token))...)
	msg = append(msg, devinEncodeField(nil, devinMetaLocaleField, 2, devinEncodeString("en"))...)
	msg = append(msg, devinEncodeField(nil, devinMetaOSField, 2, devinEncodeString("linux"))...)
	msg = append(msg, devinEncodeField(nil, devinMetaClientVerField, 2, devinEncodeString(devinClientVersion))...)
	if spec.RequestID != 0 {
		msg = append(msg, devinEncodeField(nil, devinMetaRequestIDField, 0, devinEncodeVarint(nil, spec.RequestID))...)
	}
	if spec.SessionUUID != "" {
		msg = append(msg, devinEncodeField(nil, devinMetaSessionIDField, 2, devinEncodeString(spec.SessionUUID))...)
	}
	msg = append(msg, devinEncodeField(nil, devinMetaClientKindField, 2, devinEncodeString(devinClientKind))...)
	ts := spec.Now
	if ts.IsZero() {
		ts = time.Now()
	}
	msg = append(msg, devinEncodeSubMessage(devinMetaTimestampField, devinTimestampBody(ts))...)
	if spec.UserJWT != "" {
		msg = append(msg, devinEncodeField(nil, devinMetaUserJWTField, 2, devinEncodeString(spec.UserJWT))...)
	}
	if spec.TriggerID != "" {
		msg = append(msg, devinEncodeField(nil, devinMetaTriggerIDField, 2, devinEncodeString(spec.TriggerID))...)
	}
	msg = append(msg, devinEncodeField(nil, devinMetaUnsetField, 2, devinEncodeString("Unset"))...)
	msg = append(msg, devinEncodeField(nil, devinMetaClientKind2Field, 2, devinEncodeString(devinClientKind))...)
	if spec.Fingerprint != "" {
		msg = append(msg, devinEncodeField(nil, devinMetaFingerprintField, 2, devinEncodeString(spec.Fingerprint))...)
	}
	return msg
}

// devinBuildChatRequestFull assembles a complete GetChatMessage request,
// sending every conversation turn as its own prompt so history, tool calls,
// and tool results all reach the model.
func devinBuildChatRequestFull(spec devinChatSpec) []byte {
	var msg []byte
	msg = append(msg, devinEncodeSubMessage(devinReqMetadataField, devinBuildMetadata(spec))...)
	if spec.System != "" {
		msg = append(msg, devinEncodeField(nil, devinReqSystemField, 2, devinEncodeString(spec.System))...)
	}
	for _, m := range spec.Messages {
		msg = append(msg, devinEncodeSubMessage(devinReqPromptField, devinEncodeMessagePrompt(spec.SessionUUID, m))...)
	}
	msg = append(msg, devinEncodeField(nil, devinReqTypeField, 0, devinEncodeVarint(nil, devinRequestTypeCascade))...)
	msg = append(msg, devinEncodeSubMessage(devinReqConfigField, devinEncodeCompletionConfig(spec.Config))...)
	for _, t := range spec.Tools {
		msg = append(msg, devinEncodeSubMessage(devinReqToolsField, devinEncodeToolDef(t))...)
	}
	if spec.CascadeID != "" {
		msg = append(msg, devinEncodeField(nil, devinReqCascadeField, 2, devinEncodeString(spec.CascadeID))...)
	}
	if spec.PromptID != "" {
		msg = append(msg, devinEncodeField(nil, devinReqPromptIDField, 2, devinEncodeString(spec.PromptID))...)
	}
	// tool_choice=auto, an ephemeral system prompt cache entry, and an
	// execution id are part of every native request.
	if len(spec.Tools) > 0 {
		msg = append(msg, devinEncodeSubMessage(devinReqToolChoiceField, devinEncodeToolChoiceAuto())...)
		if spec.DisableParallelTools {
			msg = append(msg, devinEncodeField(nil, devinReqParallelToolsField, 0, devinEncodeVarint(nil, 1))...)
		}
	}
	msg = append(msg, devinEncodeSubMessage(devinReqCacheOptionsField, devinEncodeCacheOptions())...)
	if spec.ExecutionID != "" {
		msg = append(msg, devinEncodeField(nil, devinReqExecutionIDField, 2, devinEncodeString(spec.ExecutionID))...)
	}
	msg = append(msg, devinEncodeField(nil, devinReqPlannerModeField, 0, devinEncodeVarint(nil, 1))...)
	msg = append(msg, devinEncodeField(nil, devinReqModelField, 2, devinEncodeString(spec.ModelUID))...)
	if spec.AssignmentJWT != "" {
		msg = append(msg, devinEncodeField(nil, devinReqAssignmentJWTField, 2, devinEncodeString(spec.AssignmentJWT))...)
	}
	out := []byte{0x00}
	var ln [4]byte
	binary.BigEndian.PutUint32(ln[:], uint32(len(msg)))
	out = append(out, ln[:]...)
	return append(out, msg...)
}

// devinRequestMessages extracts the full conversation plus the joined system
// prompt from an OpenAI-style request payload.
func devinRequestMessages(payload []byte) (msgs []devinMessage, system string) {
	if len(payload) == 0 {
		return nil, ""
	}
	all := extractDevinMessages(payload)
	var systems []string
	for _, m := range all {
		if m.role == "system" || m.role == "developer" {
			if s := strings.TrimSpace(m.content); s != "" {
				systems = append(systems, s)
			}
			continue
		}
		if strings.TrimSpace(m.content) == "" && m.toolCallID == "" && len(m.toolCalls) == 0 && m.thinking == "" {
			continue
		}
		msgs = append(msgs, m)
	}
	return msgs, strings.Join(systems, "\n\n")
}

// AssignModel resolves a router uid to a concrete model before the chat call.
const (
	devinAssignModelPath = "/exa.api_server_pb.ApiServerService/AssignModel"

	devinAssignMetadataField = 1
	devinAssignRouterField   = 2
	devinAssignCascadeField  = 3
	devinAssignPromptField   = 5

	devinAssignmentField    = 1
	devinAssignmentJWTField = 1
	devinAssignmentUIDField = 2

	devinReqAssignmentJWTField = 26
)

// devinModelAssignment is the resolved routing decision.
type devinModelAssignment struct {
	JWT      string
	ModelUID string
}

// devinBuildAssignModelRequest encodes an AssignModelRequest envelope.
func devinBuildAssignModelRequest(spec devinChatSpec, routerUID string) []byte {
	var msg []byte
	msg = append(msg, devinEncodeSubMessage(devinAssignMetadataField, devinBuildMetadata(spec))...)
	msg = append(msg, devinEncodeField(nil, devinAssignRouterField, 2, devinEncodeString(routerUID))...)
	if spec.CascadeID != "" {
		msg = append(msg, devinEncodeField(nil, devinAssignCascadeField, 2, devinEncodeString(spec.CascadeID))...)
	}
	if len(spec.Messages) > 0 {
		last := spec.Messages[len(spec.Messages)-1]
		msg = append(msg, devinEncodeSubMessage(devinAssignPromptField, devinEncodeMessagePrompt(spec.SessionUUID, last))...)
	}
	out := []byte{0x00}
	var ln [4]byte
	binary.BigEndian.PutUint32(ln[:], uint32(len(msg)))
	out = append(out, ln[:]...)
	return append(out, msg...)
}

// devinParseAssignModelResponse reads the assignment out of the response frames.
func devinParseAssignModelResponse(frames [][]byte) (devinModelAssignment, bool) {
	var out devinModelAssignment
	found := false
	for _, frame := range frames {
		devinScanFields(frame, func(num, wire int, _ uint64, data []byte) bool {
			if num != devinAssignmentField || wire != 2 {
				return true
			}
			devinScanFields(data, func(fn, fw int, _ uint64, fd []byte) bool {
				if fw != 2 {
					return true
				}
				switch fn {
				case devinAssignmentJWTField:
					out.JWT = string(fd)
					found = true
				case devinAssignmentUIDField:
					out.ModelUID = string(fd)
					found = true
				}
				return true
			})
			return true
		})
	}
	return out, found
}

// devinEncodeToolChoiceAuto encodes ChatToolChoice{option_name: "auto"}.
func devinEncodeToolChoiceAuto() []byte {
	return devinEncodeField(nil, 1, 2, devinEncodeString("auto"))
}

// devinEncodeCacheOptions encodes PromptCacheOptions{type: EPHEMERAL}.
//
// The native client marks the system prompt as an ephemeral cache entry, which
// is what makes prompt caching effective across turns.
func devinEncodeCacheOptions() []byte {
	return devinEncodeField(nil, 1, 0, devinEncodeVarint(nil, devinCacheControlEphemeral))
}

// devinStopSequences reads OpenAI-style stop values, which may be a string or
// an array of strings.
func devinStopSequences(v any) []string {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) != "" {
			return []string{t}
		}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

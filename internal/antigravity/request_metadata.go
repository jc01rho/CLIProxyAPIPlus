package antigravity

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Envelope field order for non-image agent requests (agy CLI 1.1.3).
var agyEnvelopeFieldOrder = []string{
	"project",
	"requestId",
	"request",
	"model",
	"userAgent",
	"requestType",
}

// Request payload field order inside envelope.request.
var agyRequestFieldOrder = []string{
	"contents",
	"systemInstruction",
	"tools",
	"toolConfig",
	"labels",
	"generationConfig",
	"sessionId",
}

// Labels field order (optional model_enum omitted when absent).
var agyLabelsFieldOrder = []string{
	"last_step_index",
	"model_enum",
	"trajectory_id",
	"used_claude",
	"used_claude_conservative",
	"used_non_gemini_model",
}

// Model enum mapping from agy CLI wire model names (agy-request-metadata.ts).
var agyModelEnumByWireModel = map[string]string{
	"gemini-3.5-flash-extra-low": "MODEL_PLACEHOLDER_M187",
	"gemini-3.5-flash-low":       "MODEL_PLACEHOLDER_M20",
	"gemini-3-flash-agent":       "MODEL_PLACEHOLDER_M84",
	"gemini-3.1-pro-low":         "MODEL_PLACEHOLDER_M36",
	"gemini-pro-agent":           "MODEL_PLACEHOLDER_M16",
	"claude-sonnet-4-6":          "MODEL_PLACEHOLDER_M35",
	"claude-opus-4-6-thinking":   "MODEL_PLACEHOLDER_M26",
	"gemini-3.1-flash-image":     "MODEL_PLACEHOLDER_M21",
	"gpt-oss-120b-medium":        "MODEL_OPENAI_GPT_OSS_120B_MEDIUM",
}

// AgyRequestSessionContext is the stable AGY wire identity for one conversation trajectory.
// Without OpenCode workspace context, NumericSessionID is Fnv1a64Signed of a stable
// identity source (typically project_id). used_* flags are current-request only
// unless the caller persists them across requests.
type AgyRequestSessionContext struct {
	ConversationID     string
	TrajectoryID       string
	NumericSessionID   string
	UsedClaude         bool
	UsedNonGeminiModel bool
}

// AgyAgentRequestMetadata is the agent envelope metadata applied to one request.
type AgyAgentRequestMetadata struct {
	RequestID     string
	SessionID     string
	Labels        map[string]string
	LastStepIndex int
}

// Fnv1a64Signed mirrors agy-request-metadata.ts fnv1a64Signed (signed int64 decimal).
func Fnv1a64Signed(input string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(input))
	// BigInt.asIntN(64, hash) — reinterpret as signed int64
	return strconv.FormatInt(int64(h.Sum64()), 10)
}

// GetAgyModelEnum returns the optional model_enum label for a wire model name.
func GetAgyModelEnum(model string) (string, bool) {
	v, ok := agyModelEnumByWireModel[strings.ToLower(strings.TrimSpace(model))]
	return v, ok
}

// CountAgyRequestSteps counts contents[].parts length (minimum 1).
func CountAgyRequestSteps(requestPayload []byte) int {
	contents := gjson.GetBytes(requestPayload, "contents")
	if !contents.IsArray() {
		return 1
	}
	partCount := 0
	for _, content := range contents.Array() {
		if !content.IsObject() {
			continue
		}
		parts := content.Get("parts")
		if parts.IsArray() {
			partCount += len(parts.Array())
		}
	}
	if partCount < 1 {
		return 1
	}
	return partCount
}

// BuildAgyAgentRequestMetadata builds requestId/sessionId/labels for an agent request.
// timestampMs is milliseconds. session.Used* flags are updated in-place for the current model.
func BuildAgyAgentRequestMetadata(session *AgyRequestSessionContext, requestPayload []byte, model string, timestampMs int64) AgyAgentRequestMetadata {
	if session == nil {
		session = &AgyRequestSessionContext{}
	}
	lastStep := CountAgyRequestSteps(requestPayload)
	lower := strings.ToLower(strings.TrimSpace(model))
	isClaude := strings.HasPrefix(lower, "claude-")
	isNonGemini := isClaude || strings.HasPrefix(lower, "gpt-")
	session.UsedClaude = session.UsedClaude || isClaude
	session.UsedNonGeminiModel = session.UsedNonGeminiModel || isNonGemini

	labels := map[string]string{
		"last_step_index":          strconv.Itoa(lastStep),
		"trajectory_id":            session.TrajectoryID,
		"used_claude":              boolStr(session.UsedClaude),
		"used_claude_conservative": boolStr(session.UsedClaude),
		"used_non_gemini_model":    boolStr(session.UsedNonGeminiModel),
	}
	if modelEnum, ok := GetAgyModelEnum(model); ok {
		labels["model_enum"] = modelEnum
	}

	return AgyAgentRequestMetadata{
		RequestID: "agent/" + session.ConversationID + "/" + strconv.FormatInt(timestampMs, 10) + "/" +
			session.TrajectoryID + "/" + strconv.Itoa(lastStep+1),
		SessionID:     session.NumericSessionID,
		Labels:        labels,
		LastStepIndex: lastStep,
	}
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// OrderAgyRequestPayload reorders request object keys to agy CLI order.
func OrderAgyRequestPayload(request []byte) []byte {
	return orderObjectFields(request, agyRequestFieldOrder)
}

// OrderAgyEnvelope reorders top-level agent envelope keys to agy CLI order.
func OrderAgyEnvelope(body []byte) []byte {
	return orderObjectFields(body, agyEnvelopeFieldOrder)
}

// OrderAgyLabels reorders labels object keys.
func OrderAgyLabels(labels map[string]string) []byte {
	out := []byte("{}")
	for _, key := range agyLabelsFieldOrder {
		if v, ok := labels[key]; ok {
			out, _ = sjson.SetBytes(out, key, v)
		}
	}
	known := map[string]struct{}{
		"last_step_index": {}, "model_enum": {}, "trajectory_id": {},
		"used_claude": {}, "used_claude_conservative": {}, "used_non_gemini_model": {},
	}
	for k, v := range labels {
		if _, ok := known[k]; !ok {
			out, _ = sjson.SetBytes(out, k, v)
		}
	}
	return out
}

func orderObjectFields(body []byte, fieldOrder []string) []byte {
	if len(body) == 0 {
		return body
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body
	}
	ordered := []byte("{}")
	seen := make(map[string]bool, len(fieldOrder))
	for _, field := range fieldOrder {
		if v := root.Get(field); v.Exists() {
			ordered, _ = sjson.SetRawBytes(ordered, field, []byte(v.Raw))
			seen[field] = true
		}
	}
	root.ForEach(func(key, value gjson.Result) bool {
		k := key.String()
		if seen[k] {
			return true
		}
		ordered, _ = sjson.SetRawBytes(ordered, k, []byte(value.Raw))
		return true
	})
	return ordered
}

// ApplyAgyAgentWireMetadata injects requestId, labels, sessionId and reorders
// the agent envelope for agy CLI 1.1.3 wire parity. requestType must already be "agent".
func ApplyAgyAgentWireMetadata(body []byte, session *AgyRequestSessionContext, model string, timestampMs int64) []byte {
	requestRaw := gjson.GetBytes(body, "request")
	var requestBytes []byte
	if requestRaw.Exists() {
		requestBytes = compactJSON([]byte(requestRaw.Raw))
	} else {
		requestBytes = []byte("{}")
	}

	meta := BuildAgyAgentRequestMetadata(session, requestBytes, model, timestampMs)

	labelsJSON := OrderAgyLabels(meta.Labels)
	requestBytes, _ = sjson.SetRawBytes(requestBytes, "labels", labelsJSON)
	requestBytes, _ = sjson.SetBytes(requestBytes, "sessionId", meta.SessionID)
	requestBytes = OrderAgyRequestPayload(requestBytes)

	body, _ = sjson.SetBytes(body, "requestId", meta.RequestID)
	body, _ = sjson.SetRawBytes(body, "request", requestBytes)
	// Compact + reorder top-level envelope for stable wire string equality.
	return OrderAgyEnvelope(compactJSON(body))
}

// --- Conversation session store (mirrors agy-request-metadata.ts AgyRequestSessionStore) ---

const (
	agySessionTTLMs   int64 = 24 * 60 * 60 * 1000
	agySessionMaxKeys       = 256
)

type agySessionEntry struct {
	ctx          *AgyRequestSessionContext
	lastAccessMs int64
	lastReqMs    int64
}

var (
	agySessionMu sync.Mutex
	agySessions  = map[string]*agySessionEntry{}
)

// BeginAgyRequest returns the stable session context for a conversation key plus a
// monotonic request timestamp. The same conversationId/trajectoryId and accumulated
// used_* flags are reused across turns of the same conversation (TTL 24h, max 256
// keys, monotonic timestamp), matching the reference AgyRequestSessionStore.beginRequest.
// ponytail: convKey is the first-user-message hash — distinct conversations that open
// with an identical first message share a trajectory; upgrade to a real conversation id
// if the proxy ever threads one.
func BeginAgyRequest(convKey, numericSessionID string, nowMs int64, newUUID func() string) (*AgyRequestSessionContext, int64) {
	agySessionMu.Lock()
	defer agySessionMu.Unlock()

	// Prune expired entries (never the current key).
	for k, e := range agySessions {
		if k != convKey && nowMs-e.lastAccessMs > agySessionTTLMs {
			delete(agySessions, k)
		}
	}

	e, ok := agySessions[convKey]
	if !ok {
		// Evict least-recently-accessed while at capacity.
		for len(agySessions) >= agySessionMaxKeys {
			oldestK, oldestMs, first := "", int64(0), true
			for k, v := range agySessions {
				if first || v.lastAccessMs < oldestMs {
					oldestK, oldestMs, first = k, v.lastAccessMs, false
				}
			}
			if oldestK == "" {
				break
			}
			delete(agySessions, oldestK)
		}
		e = &agySessionEntry{ctx: &AgyRequestSessionContext{
			ConversationID:   newUUID(),
			TrajectoryID:     newUUID(),
			NumericSessionID: numericSessionID,
		}}
		agySessions[convKey] = e
	}
	e.lastAccessMs = nowMs
	ts := nowMs
	if e.lastReqMs+1 > ts {
		ts = e.lastReqMs + 1
	}
	e.lastReqMs = ts
	return e.ctx, ts
}

// compactJSON removes insignificant whitespace while preserving key order
// of the first pass via sjson/gjson Raw. Falls back to original on error.
func compactJSON(b []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return b
	}
	return buf.Bytes()
}

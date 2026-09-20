package helps

import (
	"encoding/json"
	"strings"
)

const defaultWorkBuddyDeepSeekEffort = "high"

var workBuddyEffortRank = map[string]int{
	"off":     0,
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

// NormalizeWorkBuddyPayload applies the WorkBuddy outbound compatibility pipeline.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/payload.go, tool_pairing.go, and thinking.go.
func NormalizeWorkBuddyPayload(src []byte, supportedEfforts []string, defaultEffort string) ([]byte, bool) {
	if len(src) == 0 {
		return src, false
	}
	var obj map[string]any
	if err := json.Unmarshal(src, &obj); err != nil {
		return src, false
	}

	changed := false
	if stream, ok := obj["stream"].(bool); !ok || !stream {
		obj["stream"] = true
		changed = true
	}
	if _, exists := obj["stream_options"]; !exists {
		obj["stream_options"] = map[string]any{"include_usage": true}
		changed = true
	}
	changed = translateWorkBuddyMaxCompletionTokens(obj) || changed
	changed = normalizeWorkBuddyToolChoice(obj) || changed
	changed = normalizeWorkBuddyRoles(obj) || changed
	if messages, ok := obj["messages"].([]any); ok {
		var messagesChanged bool
		messages, messagesChanged = repackWorkBuddyToolResultBlocks(messages)
		changed = messagesChanged || changed
		messages, messagesChanged = cleanupWorkBuddyOrphanToolCalls(messages)
		changed = messagesChanged || changed
		if messagesChanged || changed {
			obj["messages"] = messages
		}
	}
	changed = injectWorkBuddyDeepSeekThinking(obj, defaultEffort) || changed
	changed = normalizeWorkBuddyReasoningEffort(obj, supportedEfforts) || changed
	changed = backfillWorkBuddyReasoningContent(obj) || changed

	if !changed {
		return src, false
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return src, false
	}
	return out, true
}

// translateWorkBuddyMaxCompletionTokens rewrites the positive integer alias and always removes it.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/payload.go (translateMaxCompletionTokens).
func translateWorkBuddyMaxCompletionTokens(obj map[string]any) bool {
	alias, exists := obj["max_completion_tokens"]
	if !exists {
		return false
	}
	delete(obj, "max_completion_tokens")
	if _, explicit := obj["max_tokens"]; explicit {
		return true
	}
	switch value := alias.(type) {
	case float64:
		if value > 0 && value == float64(int64(value)) {
			obj["max_tokens"] = int64(value)
		}
	case int64:
		if value > 0 {
			obj["max_tokens"] = value
		}
	case int:
		if value > 0 {
			obj["max_tokens"] = int64(value)
		}
	}
	return true
}

// normalizeWorkBuddyToolChoice converts OpenAI object forms to WorkBuddy's string field.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/payload.go (normalizeToolChoice).
func normalizeWorkBuddyToolChoice(obj map[string]any) bool {
	choice, exists := obj["tool_choice"]
	if !exists {
		return false
	}
	suppressTools := func() bool {
		changed := false
		if _, ok := obj["tools"]; ok {
			delete(obj, "tools")
			changed = true
		}
		if _, ok := obj["functions"]; ok {
			delete(obj, "functions")
			changed = true
		}
		return changed
	}

	switch value := choice.(type) {
	case string:
		if !strings.EqualFold(strings.TrimSpace(value), "none") {
			return false
		}
		delete(obj, "tool_choice")
		suppressTools()
		return true
	case map[string]any:
		typ, _ := value["type"].(string)
		typ = strings.ToLower(strings.TrimSpace(typ))
		switch typ {
		case "none":
			delete(obj, "tool_choice")
			suppressTools()
			return true
		case "auto", "required":
			obj["tool_choice"] = typ
			return true
		case "function":
			name := ""
			if function, ok := value["function"].(map[string]any); ok {
				name, _ = function["name"].(string)
			}
			if name == "" {
				name, _ = value["name"].(string)
			}
			name = strings.TrimSpace(name)
			if name == "" {
				name = "auto"
			}
			obj["tool_choice"] = name
			return true
		default:
			delete(obj, "tool_choice")
			return true
		}
	default:
		delete(obj, "tool_choice")
		return true
	}
}

// normalizeWorkBuddyRoles maps only the unsupported developer role to system.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/payload.go (normalizeRoles).
func normalizeWorkBuddyRoles(obj map[string]any) bool {
	messages, ok := obj["messages"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, ok := message["role"].(string)
		if ok && strings.EqualFold(strings.TrimSpace(role), "developer") {
			message["role"] = "system"
			changed = true
		}
	}
	return changed
}

// repackWorkBuddyToolResultBlocks moves interleaved non-tool messages after matching result blocks.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/tool_pairing.go (repackToolResultBlocks).
func repackWorkBuddyToolResultBlocks(messages []any) ([]any, bool) {
	if len(messages) < 3 {
		return messages, false
	}
	out := make([]any, 0, len(messages))
	changed := false
	for i := 0; i < len(messages); {
		message, ok := messages[i].(map[string]any)
		if !ok || message["role"] != "assistant" {
			out = append(out, messages[i])
			i++
			continue
		}
		calls, ok := message["tool_calls"].([]any)
		if !ok || len(calls) == 0 {
			out = append(out, messages[i])
			i++
			continue
		}
		wanted := make(map[string]bool, len(calls))
		for _, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			if id, _ := call["id"].(string); id != "" {
				wanted[id] = true
			}
		}

		out = append(out, messages[i])
		i++
		var results []any
		var interleaved []any
		sawNonTool := false
		for i < len(messages) {
			candidate, ok := messages[i].(map[string]any)
			if !ok {
				break
			}
			role, _ := candidate["role"].(string)
			if role == "tool" {
				id, _ := candidate["tool_call_id"].(string)
				if !wanted[id] {
					break
				}
				results = append(results, messages[i])
				if sawNonTool {
					changed = true
				}
				i++
				continue
			}
			if len(results) == 0 {
				break
			}
			if role == "assistant" {
				if nextCalls, _ := candidate["tool_calls"].([]any); len(nextCalls) > 0 {
					break
				}
			}
			interleaved = append(interleaved, messages[i])
			sawNonTool = true
			i++
		}
		out = append(out, results...)
		out = append(out, interleaved...)
	}
	if !changed {
		return messages, false
	}
	return out, true
}

// cleanupWorkBuddyOrphanToolCalls symmetrically retains only call IDs present on both sides.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/tool_pairing.go (cleanupOrphanToolCalls).
func cleanupWorkBuddyOrphanToolCalls(messages []any) ([]any, bool) {
	if len(messages) == 0 {
		return messages, false
	}
	callIDs := make(map[string]bool)
	resultIDs := make(map[string]bool)
	hasTraffic := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch message["role"] {
		case "tool":
			if id, ok := message["tool_call_id"].(string); ok && id != "" {
				resultIDs[id] = true
				hasTraffic = true
			}
		case "assistant":
			calls, _ := message["tool_calls"].([]any)
			for _, rawCall := range calls {
				call, ok := rawCall.(map[string]any)
				if !ok {
					continue
				}
				if id, ok := call["id"].(string); ok && id != "" {
					callIDs[id] = true
					hasTraffic = true
				}
			}
		}
	}
	if !hasTraffic {
		return messages, false
	}
	keepCalls := make(map[string]bool)
	for id := range callIDs {
		if resultIDs[id] {
			keepCalls[id] = true
		}
	}

	changed := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || message["role"] != "assistant" {
			continue
		}
		calls, ok := message["tool_calls"].([]any)
		if !ok || len(calls) == 0 {
			continue
		}
		kept := make([]any, 0, len(calls))
		for _, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			if id, _ := call["id"].(string); keepCalls[id] {
				kept = append(kept, call)
			}
		}
		if len(kept) == len(calls) {
			continue
		}
		changed = true
		if len(kept) == 0 {
			delete(message, "tool_calls")
		} else {
			message["tool_calls"] = kept
		}
	}

	keptMessages := make([]any, 0, len(messages))
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			keptMessages = append(keptMessages, raw)
			continue
		}
		if message["role"] == "tool" {
			id, _ := message["tool_call_id"].(string)
			if !keepCalls[id] {
				changed = true
				continue
			}
		}
		keptMessages = append(keptMessages, raw)
	}
	if !changed {
		return messages, false
	}
	return keptMessages, true
}

// injectWorkBuddyDeepSeekThinking enables DeepSeek thinking and enforces disabled-effort semantics.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/thinking.go (injectThinking).
func injectWorkBuddyDeepSeekThinking(obj map[string]any, defaultEffort string) bool {
	model, _ := obj["model"].(string)
	if !isWorkBuddyDeepSeekModel(model) {
		return false
	}
	thinking, isObject := obj["thinking"].(map[string]any)
	typ := ""
	if isObject {
		typ, _ = thinking["type"].(string)
		typ = strings.TrimSpace(typ)
	}
	if typ != "" {
		if strings.EqualFold(typ, "disabled") {
			changed := false
			if _, exists := obj["reasoning_effort"]; exists {
				delete(obj, "reasoning_effort")
				changed = true
			}
			if _, exists := obj["reasoningEffort"]; exists {
				delete(obj, "reasoningEffort")
				changed = true
			}
			return changed
		}
		return ensureWorkBuddyDeepSeekEffort(obj, defaultEffort)
	}
	if !isObject {
		obj["thinking"] = map[string]any{"type": "enabled"}
	} else {
		thinking["type"] = "enabled"
	}
	ensureWorkBuddyDeepSeekEffort(obj, defaultEffort)
	return true
}

// ensureWorkBuddyDeepSeekEffort adds the model default or the reference fallback without overwriting explicit fields.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/thinking.go (ensureDeepSeekEffort).
func ensureWorkBuddyDeepSeekEffort(obj map[string]any, defaultEffort string) bool {
	if _, exists := obj["reasoning_effort"]; exists {
		return false
	}
	if _, exists := obj["reasoningEffort"]; exists {
		return false
	}
	if strings.TrimSpace(defaultEffort) == "" {
		defaultEffort = defaultWorkBuddyDeepSeekEffort
	}
	obj["reasoning_effort"] = defaultEffort
	return true
}

// isWorkBuddyDeepSeekModel reports whether a model uses WorkBuddy's DeepSeek thinking format.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/thinking.go (isDeepSeekModel).
func isWorkBuddyDeepSeekModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "deepseek")
}

// normalizeWorkBuddyReasoningEffort downgrades explicit known levels to the nearest supported level.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/payload.go (normalizeReasoningEffort).
func normalizeWorkBuddyReasoningEffort(obj map[string]any, supportedEfforts []string) bool {
	if len(supportedEfforts) == 0 {
		return false
	}
	key := ""
	if _, exists := obj["reasoning_effort"]; exists {
		key = "reasoning_effort"
	} else if _, exists := obj["reasoningEffort"]; exists {
		key = "reasoningEffort"
	} else {
		return false
	}
	requested, ok := obj[key].(string)
	if !ok {
		return false
	}
	requested = strings.ToLower(strings.TrimSpace(requested))
	requestedRank, known := workBuddyEffortRank[requested]
	if !known {
		return false
	}

	best, bestRank := "", -1
	for _, supported := range supportedEfforts {
		rank, ok := workBuddyEffortRank[strings.ToLower(strings.TrimSpace(supported))]
		if ok && rank <= requestedRank && rank > bestRank {
			best, bestRank = supported, rank
		}
	}
	if best != "" {
		if strings.EqualFold(best, requested) {
			return false
		}
		obj[key] = best
		return true
	}

	lowest, lowestRank := "", int(^uint(0)>>1)
	for _, supported := range supportedEfforts {
		rank, ok := workBuddyEffortRank[strings.ToLower(strings.TrimSpace(supported))]
		if ok && rank < lowestRank {
			lowest, lowestRank = supported, rank
		}
	}
	if lowest == "" || strings.EqualFold(lowest, requested) {
		return false
	}
	obj[key] = lowest
	return true
}

// backfillWorkBuddyReasoningContent guarantees string reasoning_content on gated DeepSeek assistant messages.
// Reference: Sliverkiss/workbuddy2api, internal/upstream/thinking.go (backfillReasoningContent).
func backfillWorkBuddyReasoningContent(obj map[string]any) bool {
	model, _ := obj["model"].(string)
	if !isWorkBuddyDeepSeekModel(model) {
		return false
	}
	messages, ok := obj["messages"].([]any)
	if !ok || len(messages) == 0 {
		return false
	}

	thinkingEnabled := false
	if thinking, ok := obj["thinking"].(map[string]any); ok {
		typ, _ := thinking["type"].(string)
		thinkingEnabled = strings.EqualFold(strings.TrimSpace(typ), "enabled")
	}
	hasTrace := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if reasoning, ok := message["reasoning"].(string); ok && reasoning != "" {
			hasTrace = true
			break
		}
		if _, exists := message["reasoning_content"]; exists {
			hasTrace = true
			break
		}
	}
	if !thinkingEnabled && !hasTrace {
		return false
	}

	changed := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || message["role"] != "assistant" {
			continue
		}
		reasoningContent, hasReasoningContent := message["reasoning_content"].(string)
		if !hasReasoningContent {
			if reasoning, ok := message["reasoning"].(string); ok {
				reasoningContent = reasoning
			} else {
				reasoningContent = ""
			}
			message["reasoning_content"] = reasoningContent
			changed = true
		}
		if reasoning, ok := message["reasoning"].(string); ok && reasoning != "" {
			continue
		}
		if reasoningContent != "" {
			message["reasoning"] = reasoningContent
		} else {
			message["reasoning"] = " "
		}
		changed = true
	}
	return changed
}

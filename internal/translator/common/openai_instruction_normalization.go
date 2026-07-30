package common

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// InstructionNormalizationReason describes why an OpenAI instruction prefix was or was not normalized.
type InstructionNormalizationReason string

const (
	InstructionNormalizationReasonNoDeveloper         InstructionNormalizationReason = "no_developer"
	InstructionNormalizationReasonNormalized          InstructionNormalizationReason = "normalized_leading_developer"
	InstructionNormalizationReasonLaterDeveloper      InstructionNormalizationReason = "developer_after_conversation"
	InstructionNormalizationReasonNonLeadingDeveloper InstructionNormalizationReason = "non_leading_developer"
	InstructionNormalizationReasonInvalidJSON         InstructionNormalizationReason = "invalid_json"
	InstructionNormalizationReasonMissingMessages     InstructionNormalizationReason = "missing_messages"
	InstructionNormalizationReasonMessagesNotArray    InstructionNormalizationReason = "messages_not_array"
)

// InstructionNormalizationResult reports developer-role handling without exposing instruction content.
type InstructionNormalizationResult struct {
	Changed             bool
	HadDeveloper        bool
	RemovedAllDeveloper bool
	Reason              InstructionNormalizationReason
}

// NormalizeLeadingOpenAIInstructions merges a contiguous leading system/developer
// message prefix into one system message. A developer message after a conversational
// turn makes the payload unsafe to rewrite and is left unchanged.
func NormalizeLeadingOpenAIInstructions(payload []byte) ([]byte, InstructionNormalizationResult, error) {
	result := InstructionNormalizationResult{Reason: InstructionNormalizationReasonNoDeveloper}
	if !gjson.ValidBytes(payload) {
		result.Reason = InstructionNormalizationReasonInvalidJSON
		return payload, result, nil
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() {
		result.Reason = InstructionNormalizationReasonMissingMessages
		return payload, result, nil
	}
	if !messages.IsArray() {
		result.Reason = InstructionNormalizationReasonMessagesNotArray
		return payload, result, nil
	}

	items := messages.Array()
	prefixEnd := 0
	for prefixEnd < len(items) {
		role := items[prefixEnd].Get("role").String()
		if role != "system" && role != "developer" {
			break
		}
		prefixEnd++
	}

	seenConversation := false
	developerOutsidePrefix := false
	for index, message := range items {
		role := message.Get("role").String()
		switch role {
		case "user", "assistant", "tool", "function":
			seenConversation = true
		case "developer":
			result.HadDeveloper = true
			if seenConversation {
				result.Reason = InstructionNormalizationReasonLaterDeveloper
				return payload, result, nil
			}
			if index >= prefixEnd {
				developerOutsidePrefix = true
			}
		}
	}
	if !result.HadDeveloper {
		return payload, result, nil
	}
	if developerOutsidePrefix {
		result.Reason = InstructionNormalizationReasonNonLeadingDeveloper
		return payload, result, nil
	}

	useArray := false
	for _, message := range items[:prefixEnd] {
		content := message.Get("content")
		if content.Type != gjson.String || message.Get("cache_control").Exists() {
			useArray = true
			break
		}
	}

	merged := []byte(`{"role":"system","content":""}`)
	if useArray {
		parts := make([][]byte, 0, prefixEnd)
		for _, message := range items[:prefixEnd] {
			content := message.Get("content")
			start := len(parts)
			switch {
			case content.Type == gjson.String:
				part := []byte(`{"type":"text","text":""}`)
				part, _ = sjson.SetBytes(part, "text", content.String())
				parts = append(parts, part)
			case content.IsArray():
				content.ForEach(func(_, part gjson.Result) bool {
					parts = append(parts, []byte(part.Raw))
					return true
				})
			case content.IsObject():
				parts = append(parts, []byte(content.Raw))
			}

			if cacheControl := message.Get("cache_control"); cacheControl.Exists() {
				if len(parts) == start {
					parts = append(parts, []byte(`{"type":"text","text":""}`))
				}
				last := len(parts) - 1
				if gjson.ParseBytes(parts[last]).IsObject() {
					if !gjson.GetBytes(parts[last], "cache_control").Exists() {
						parts[last], _ = sjson.SetRawBytes(parts[last], "cache_control", []byte(cacheControl.Raw))
					}
				} else {
					cachePart := []byte(`{"type":"text","text":""}`)
					cachePart, _ = sjson.SetRawBytes(cachePart, "cache_control", []byte(cacheControl.Raw))
					parts = append(parts, cachePart)
				}
			}
		}
		merged, _ = sjson.SetRawBytes(merged, "content", JoinRawArray(parts))
	} else {
		texts := make([]string, 0, prefixEnd)
		for _, message := range items[:prefixEnd] {
			texts = append(texts, message.Get("content").String())
		}
		merged, _ = sjson.SetBytes(merged, "content", strings.Join(texts, "\n\n"))
	}

	normalizedMessages := make([][]byte, 0, len(items)-prefixEnd+1)
	normalizedMessages = append(normalizedMessages, merged)
	for _, message := range items[prefixEnd:] {
		normalizedMessages = append(normalizedMessages, []byte(message.Raw))
	}

	out, err := sjson.SetRawBytes(payload, "messages", JoinRawArray(normalizedMessages))
	if err != nil {
		return payload, result, err
	}
	result.Changed = true
	result.RemovedAllDeveloper = true
	result.Reason = InstructionNormalizationReasonNormalized
	return out, result, nil
}

package executor

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const toolCallReasoningFallback = "[reasoning unavailable]"

func normalizeOpenAIToolCallReasoningContent(body []byte, requireReasoningSignal bool) ([]byte, int, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, 0, nil
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, 0, nil
	}

	msgs := messages.Array()
	if requireReasoningSignal && !hasOpenAIReasoningSignal(body, msgs) {
		return body, 0, nil
	}

	out := body
	patched := 0
	latestReasoning := ""
	hasLatestReasoning := false

	for msgIdx, msg := range msgs {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}

		reasoning := msg.Get("reasoning_content")
		if reasoning.Exists() {
			reasoningText := strings.TrimSpace(reasoning.String())
			if reasoningText != "" {
				latestReasoning = reasoning.String()
				hasLatestReasoning = true
			}
		}

		toolCalls := msg.Get("tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() || len(toolCalls.Array()) == 0 {
			continue
		}
		if reasoning.Exists() && strings.TrimSpace(reasoning.String()) != "" {
			continue
		}

		reasoningText := fallbackOpenAIToolCallReasoning(msg, hasLatestReasoning, latestReasoning)
		path := fmt.Sprintf("messages.%d.reasoning_content", msgIdx)
		next, err := sjson.SetBytes(out, path, reasoningText)
		if err != nil {
			return body, 0, fmt.Errorf("failed to set assistant reasoning_content: %w", err)
		}
		out = next
		patched++
	}

	return out, patched, nil
}

func hasOpenAIReasoningSignal(body []byte, msgs []gjson.Result) bool {
	if value := gjson.GetBytes(body, "reasoning_effort"); value.Exists() && strings.TrimSpace(value.String()) != "" {
		return true
	}
	if value := gjson.GetBytes(body, "reasoning"); value.Exists() && value.Type != gjson.Null {
		return true
	}
	if value := gjson.GetBytes(body, "thinking"); value.Exists() && value.Type != gjson.Null {
		if strings.EqualFold(strings.TrimSpace(value.Get("type").String()), "disabled") {
			return false
		}
		return true
	}
	for _, msg := range msgs {
		if value := msg.Get("reasoning_content"); value.Exists() && strings.TrimSpace(value.String()) != "" {
			return true
		}
	}
	return false
}

func fallbackOpenAIToolCallReasoning(msg gjson.Result, hasLatest bool, latest string) string {
	if hasLatest && strings.TrimSpace(latest) != "" {
		return latest
	}

	content := msg.Get("content")
	if content.Type == gjson.String {
		if text := strings.TrimSpace(content.String()); text != "" {
			return text
		}
	}
	if content.IsArray() {
		parts := make([]string, 0, len(content.Array()))
		for _, item := range content.Array() {
			text := strings.TrimSpace(item.Get("text").String())
			if text == "" {
				continue
			}
			parts = append(parts, text)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}

	return toolCallReasoningFallback
}

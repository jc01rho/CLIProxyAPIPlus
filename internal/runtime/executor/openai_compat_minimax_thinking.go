package executor

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type minimaxThinkingTagPair struct {
	open      string
	closeTags []string
}

var minimaxThinkingTagPairs = [...]minimaxThinkingTagPair{
	{open: "<think>", closeTags: []string{"</think>"}},
	{open: "<thinking>", closeTags: []string{"</thinking>", "</response>"}},
}

var standardThinkingTagPairs = [...]minimaxThinkingTagPair{
	{open: "<thinking>", closeTags: []string{"</thinking>"}},
}

// isMiniMaxThinkingTagModel reports whether the model emits reasoning as
// inline thinking tags in the OpenAI content field.
//
// grok-4.6 (via the OpenAI-compatible local proxy path) exhibits the same
// behavior: reasoning arrives as literal <think>...</think> tags inside
// choices[].message.content instead of a dedicated reasoning_content field.
// Reuse the MiniMax tag-splitting logic for it rather than duplicating a
// near-identical detector/splitter pair.
func isMiniMaxThinkingTagModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "minimax") || strings.Contains(normalized, "grok")
}

func splitMiniMaxThinking(content string) (reasoning, cleaned string) {
	return splitThinking(content, minimaxThinkingTagPairs[:])
}

func splitThinking(content string, tagPairs []minimaxThinkingTagPair) (reasoning, cleaned string) {
	var reasoningBuilder, contentBuilder strings.Builder
	rest := content
	for {
		openIdx, pair, found := findThinkingOpenTag(rest, tagPairs)
		if !found {
			contentBuilder.WriteString(rest)
			break
		}
		contentBuilder.WriteString(rest[:openIdx])
		rest = rest[openIdx+len(pair.open):]
		closeIdx, closeTag := findMiniMaxThinkingCloseTag(rest, pair.closeTags)
		if closeIdx < 0 {
			reasoningBuilder.WriteString(rest)
			break
		}
		reasoningBuilder.WriteString(rest[:closeIdx])
		rest = rest[closeIdx+len(closeTag):]
		for strings.HasPrefix(rest, closeTag) {
			rest = rest[len(closeTag):]
		}
	}
	return reasoningBuilder.String(), contentBuilder.String()
}

func findMiniMaxThinkingCloseTag(content string, closeTags []string) (int, string) {
	firstIdx := -1
	firstTag := ""
	for _, closeTag := range closeTags {
		idx := strings.Index(content, closeTag)
		if idx >= 0 && (firstIdx < 0 || idx < firstIdx) {
			firstIdx = idx
			firstTag = closeTag
		}
	}
	return firstIdx, firstTag
}

func findMiniMaxThinkingOpenTag(content string) (int, minimaxThinkingTagPair, bool) {
	return findThinkingOpenTag(content, minimaxThinkingTagPairs[:])
}

func findThinkingOpenTag(content string, tagPairs []minimaxThinkingTagPair) (int, minimaxThinkingTagPair, bool) {
	firstIdx := -1
	var firstPair minimaxThinkingTagPair
	for _, pair := range tagPairs {
		idx := strings.Index(content, pair.open)
		if idx >= 0 && (firstIdx < 0 || idx < firstIdx) {
			firstIdx = idx
			firstPair = pair
		}
	}
	return firstIdx, firstPair, firstIdx >= 0
}

func trailingMiniMaxOpenTagPrefixLength(content string) int {
	return trailingThinkingOpenTagPrefixLength(content, minimaxThinkingTagPairs[:])
}

func trailingThinkingOpenTagPrefixLength(content string, tagPairs []minimaxThinkingTagPair) int {
	longest := 0
	for _, pair := range tagPairs {
		maxLen := min(len(content), len(pair.open)-1)
		for length := maxLen; length > longest; length-- {
			if strings.HasSuffix(content, pair.open[:length]) {
				longest = length
				break
			}
		}
	}
	return longest
}

// normalizeMiniMaxThinkingBody rewrites an OpenAI chat.completions body so that
// assistant reasoning wrapped in MiniMax thinking tags is moved from
// `choices[].message.content` into `choices[].message.reasoning_content`.
func normalizeMiniMaxThinkingBody(body []byte) []byte {
	return normalizeThinkingBody(body, minimaxThinkingTagPairs[:])
}

func normalizeStandardThinkingBody(body []byte) []byte {
	return normalizeThinkingBody(body, standardThinkingTagPairs[:])
}

func normalizeThinkingBody(body []byte, tagPairs []minimaxThinkingTagPair) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	choices := gjson.GetBytes(body, "choices")
	out := body
	modified := false
	if choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			idx := choice.Get("index")
			content := choice.Get("message.content")
			if !content.Exists() || content.Type != gjson.String {
				return true
			}
			reasoning, cleaned := splitThinking(content.String(), tagPairs)
			if reasoning == "" && cleaned == content.String() {
				return true
			}
			prefix := "choices."
			if idx.Exists() {
				prefix += idx.String() + "."
			} else {
				prefix += "0."
			}
			if cleaned != content.String() {
				if updated, err := sjson.SetBytes(out, prefix+"message.content", cleaned); err == nil {
					out = updated
					modified = true
				}
			}
			if reasoning != "" {
				if updated, err := sjson.SetBytes(out, prefix+"message.reasoning_content", reasoning); err == nil {
					out = updated
					modified = true
				}
			}
			return true
		})
	}
	output := gjson.GetBytes(body, "output")
	if output.Exists() && output.IsArray() {
		output.ForEach(func(outputIdx, item gjson.Result) bool {
			content := item.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIdx, part gjson.Result) bool {
				if part.Get("type").String() != "output_text" {
					return true
				}
				text := part.Get("text")
				if !text.Exists() || text.Type != gjson.String {
					return true
				}
				_, cleaned := splitThinking(text.String(), tagPairs)
				if cleaned == text.String() {
					return true
				}
				path := "output." + outputIdx.String() + ".content." + contentIdx.String() + ".text"
				if updated, err := sjson.SetBytes(out, path, cleaned); err == nil {
					out = updated
					modified = true
				}
				return true
			})
			return true
		})
	}
	if !modified {
		return body
	}
	return out
}

// minimaxThinkingStreamState holds per-request accumulation state for the
// streaming path so reasoning tags split across SSE frames still resolve into a
// reasoning_content delta and real content into a content delta.
type minimaxThinkingStreamState struct {
	inThinking bool
	content    strings.Builder
	pending    strings.Builder
	closeTags  []string
	tagPairs   []minimaxThinkingTagPair
}

func (s *minimaxThinkingStreamState) thinkingTagPairs() []minimaxThinkingTagPair {
	if len(s.tagPairs) != 0 {
		return s.tagPairs
	}
	return minimaxThinkingTagPairs[:]
}

func (s *minimaxThinkingStreamState) feed(fragment string) (reasoning, content string) {
	if s.inThinking {
		s.content.WriteString(fragment)
		combined := s.content.String()
		closeIdx, closeTag := findMiniMaxThinkingCloseTag(combined, s.closeTags)
		if closeIdx < 0 {
			return "", ""
		}
		reasoning = combined[:closeIdx]
		s.content.Reset()
		s.inThinking = false
		after := combined[closeIdx+len(closeTag):]
		for strings.HasPrefix(after, closeTag) {
			after = after[len(closeTag):]
		}
		s.closeTags = nil
		tailReasoning, tailContent := s.feed(after)
		return reasoning + tailReasoning, tailContent
	}

	if s.pending.Len() > 0 {
		s.pending.WriteString(fragment)
		fragment = s.pending.String()
		s.pending.Reset()
	}
	openIdx, pair, found := findThinkingOpenTag(fragment, s.thinkingTagPairs())
	if !found {
		prefixLen := trailingThinkingOpenTagPrefixLength(fragment, s.thinkingTagPairs())
		if prefixLen > 0 {
			s.pending.WriteString(fragment[len(fragment)-prefixLen:])
			fragment = fragment[:len(fragment)-prefixLen]
		}
		return "", fragment
	}
	content = fragment[:openIdx]
	s.inThinking = true
	s.closeTags = pair.closeTags
	after := fragment[openIdx+len(pair.open):]
	if after != "" {
		tailReasoning, tailContent := s.feed(after)
		return tailReasoning, content + tailContent
	}
	return "", content
}

func (s *minimaxThinkingStreamState) flush() (reasoning, content string) {
	if s.inThinking {
		reasoning = s.content.String()
	} else {
		content = s.pending.String()
	}
	s.content.Reset()
	s.pending.Reset()
	s.inThinking = false
	s.closeTags = nil
	return reasoning, content
}

// normalizeMiniMaxThinkingStream rewrites an OpenAI chat.completions SSE data
// frame (without the "data:" prefix) so thinking tags in delta content are
// routed into delta.reasoning_content. It mutates state to track thinking
// across frames.
func normalizeMiniMaxThinkingStream(state *minimaxThinkingStreamState, frame []byte) []byte {
	if len(frame) == 0 || !gjson.ValidBytes(frame) {
		return frame
	}
	choices := gjson.GetBytes(frame, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return normalizeResponsesThinkingStream(state, frame)
	}
	out := frame
	modified := false
	choices.ForEach(func(_, choice gjson.Result) bool {
		idx := choice.Get("index")
		content := choice.Get("delta.content")
		if !content.Exists() || content.Type != gjson.String {
			return true
		}
		prefix := "choices."
		if idx.Exists() {
			prefix += idx.String() + "."
		} else {
			prefix += "0."
		}
		reasoning, newContent := state.feed(content.String())
		if newContent != content.String() {
			if updated, err := sjson.SetBytes(out, prefix+"delta.content", newContent); err == nil {
				out = updated
				modified = true
			}
		}
		if reasoning != "" {
			var updated []byte
			var err error
			if existing := gjson.GetBytes(out, prefix+"delta.reasoning_content"); existing.Exists() && existing.Type == gjson.String && existing.String() != "" {
				updated, err = sjson.SetBytes(out, prefix+"delta.reasoning_content", existing.String()+reasoning)
			} else {
				updated, err = sjson.SetBytes(out, prefix+"delta.reasoning_content", reasoning)
			}
			if err == nil {
				out = updated
				modified = true
			}
		}
		return true
	})
	if !modified {
		return frame
	}
	return out
}

func normalizeResponsesThinkingStream(state *minimaxThinkingStreamState, frame []byte) []byte {
	eventType := gjson.GetBytes(frame, "type").String()
	field := ""
	switch eventType {
	case "response.output_text.delta":
		field = "delta"
	case "response.output_text.done":
		field = "text"
	default:
		return frame
	}
	value := gjson.GetBytes(frame, field)
	if !value.Exists() || value.Type != gjson.String {
		return frame
	}
	_, cleaned := state.feed(value.String())
	if cleaned == value.String() {
		return frame
	}
	updated, err := sjson.SetBytes(frame, field, cleaned)
	if err != nil {
		return frame
	}
	return updated
}

func buildMiniMaxThinkingFlushFrame(base []byte, reasoning, content string) []byte {
	frame := []byte(`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	for _, field := range []string{"id", "created", "model"} {
		if v := gjson.GetBytes(base, field); v.Exists() {
			if updated, err := sjson.SetRawBytes(frame, field, []byte(v.Raw)); err == nil {
				frame = updated
			}
		}
	}
	if reasoning != "" {
		if updated, err := sjson.SetBytes(frame, "choices.0.delta.reasoning_content", reasoning); err == nil {
			frame = updated
		}
	}
	if content != "" {
		if updated, err := sjson.SetBytes(frame, "choices.0.delta.content", content); err == nil {
			frame = updated
		}
	}
	return frame
}

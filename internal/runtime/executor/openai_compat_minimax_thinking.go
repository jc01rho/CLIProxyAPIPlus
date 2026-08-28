package executor

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type minimaxThinkingTagPair struct {
	open  string
	close string
}

var minimaxThinkingTagPairs = [...]minimaxThinkingTagPair{
	{open: "<think>", close: "</think>"},
	{open: "<thinking>", close: "</response>"},
}

// isMiniMaxThinkingTagModel reports whether the model emits reasoning as
// inline thinking tags in the OpenAI content field.
func isMiniMaxThinkingTagModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "minimax")
}

func splitMiniMaxThinking(content string) (reasoning, cleaned string) {
	var reasoningBuilder, contentBuilder strings.Builder
	rest := content
	for {
		openIdx, pair, found := findMiniMaxThinkingOpenTag(rest)
		if !found {
			contentBuilder.WriteString(rest)
			break
		}
		contentBuilder.WriteString(rest[:openIdx])
		rest = rest[openIdx+len(pair.open):]
		closeIdx := strings.Index(rest, pair.close)
		if closeIdx < 0 {
			reasoningBuilder.WriteString(rest)
			break
		}
		reasoningBuilder.WriteString(rest[:closeIdx])
		rest = rest[closeIdx+len(pair.close):]
	}
	return reasoningBuilder.String(), contentBuilder.String()
}

func findMiniMaxThinkingOpenTag(content string) (int, minimaxThinkingTagPair, bool) {
	firstIdx := -1
	var firstPair minimaxThinkingTagPair
	for _, pair := range minimaxThinkingTagPairs {
		idx := strings.Index(content, pair.open)
		if idx >= 0 && (firstIdx < 0 || idx < firstIdx) {
			firstIdx = idx
			firstPair = pair
		}
	}
	return firstIdx, firstPair, firstIdx >= 0
}

func trailingMiniMaxOpenTagPrefixLength(content string) int {
	longest := 0
	for _, pair := range minimaxThinkingTagPairs {
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
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	choices := gjson.GetBytes(body, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return body
	}
	out := body
	modified := false
	choices.ForEach(func(_, choice gjson.Result) bool {
		idx := choice.Get("index")
		content := choice.Get("message.content")
		if !content.Exists() || content.Type != gjson.String {
			return true
		}
		reasoning, cleaned := splitMiniMaxThinking(content.String())
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
	closeTag   string
}

func (s *minimaxThinkingStreamState) feed(fragment string) (reasoning, content string) {
	if s.inThinking {
		s.content.WriteString(fragment)
		combined := s.content.String()
		closeIdx := strings.Index(combined, s.closeTag)
		if closeIdx < 0 {
			return "", ""
		}
		reasoning = combined[:closeIdx]
		s.content.Reset()
		s.inThinking = false
		after := combined[closeIdx+len(s.closeTag):]
		s.closeTag = ""
		tailReasoning, tailContent := s.feed(after)
		return reasoning + tailReasoning, tailContent
	}

	if s.pending.Len() > 0 {
		s.pending.WriteString(fragment)
		fragment = s.pending.String()
		s.pending.Reset()
	}
	openIdx, pair, found := findMiniMaxThinkingOpenTag(fragment)
	if !found {
		prefixLen := trailingMiniMaxOpenTagPrefixLength(fragment)
		if prefixLen > 0 {
			s.pending.WriteString(fragment[len(fragment)-prefixLen:])
			fragment = fragment[:len(fragment)-prefixLen]
		}
		return "", fragment
	}
	content = fragment[:openIdx]
	s.inThinking = true
	s.closeTag = pair.close
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
	s.closeTag = ""
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
		return frame
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

package chat_completions

import "strings"

// grok-4.6 (and MiniMax) emit reasoning as literal <think>...</think> (or
// <thinking>...</response>) tags embedded in the answer text instead of a
// dedicated reasoning field. The xAI Responses API path
// (response.output_text.delta / message.content in this translator) carries
// the same behavior, so the tags must be split out here too. This mirrors
// internal/runtime/executor/openai_compat_minimax_thinking.go's algorithm for
// the OpenAI Chat Completions source shape; it is duplicated locally rather
// than shared to avoid coupling this stateless translator package to the
// executor package (see internal/translator/AGENTS.md: translators must not
// depend on executor-side logic) and to avoid touching that file's other
// MiniMax tool-call/history behavior.
type codexThinkingTagPair struct {
	open  string
	close string
}

var codexThinkingTagPairs = [...]codexThinkingTagPair{
	{open: "<think>", close: "</think>"},
	{open: "<thinking>", close: "</response>"},
}

// isCodexThinkingTagModel reports whether the model emits reasoning as
// inline thinking tags in the answer text rather than a dedicated reasoning
// field.
func isCodexThinkingTagModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "minimax") || strings.Contains(normalized, "grok")
}

func findCodexThinkingOpenTag(content string) (int, codexThinkingTagPair, bool) {
	firstIdx := -1
	var firstPair codexThinkingTagPair
	for _, pair := range codexThinkingTagPairs {
		idx := strings.Index(content, pair.open)
		if idx >= 0 && (firstIdx < 0 || idx < firstIdx) {
			firstIdx = idx
			firstPair = pair
		}
	}
	return firstIdx, firstPair, firstIdx >= 0
}

func trailingCodexOpenTagPrefixLength(content string) int {
	longest := 0
	for _, pair := range codexThinkingTagPairs {
		maxLen := len(content)
		if len(pair.open)-1 < maxLen {
			maxLen = len(pair.open) - 1
		}
		for length := maxLen; length > longest; length-- {
			if strings.HasSuffix(content, pair.open[:length]) {
				longest = length
				break
			}
		}
	}
	return longest
}

// splitCodexThinking splits a complete (non-streamed) text into reasoning and
// visible content by removing thinking tags.
func splitCodexThinking(content string) (reasoning, cleaned string) {
	var reasoningBuilder, contentBuilder strings.Builder
	rest := content
	for {
		openIdx, pair, found := findCodexThinkingOpenTag(rest)
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

// codexThinkingTagState holds per-stream accumulation state so thinking tags
// split across upstream SSE frames still resolve into a reasoning delta and a
// separate content delta.
type codexThinkingTagState struct {
	inThinking bool
	content    strings.Builder
	pending    strings.Builder
	closeTag   string
}

// feed processes one incoming content fragment and returns the reasoning and
// visible-content portions ready to emit for it. Text held back because it
// might be the prefix of an opening tag is buffered internally and released
// once disambiguated by a later fragment or by flush.
func (s *codexThinkingTagState) feed(fragment string) (reasoning, content string) {
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
	openIdx, pair, found := findCodexThinkingOpenTag(fragment)
	if !found {
		prefixLen := trailingCodexOpenTagPrefixLength(fragment)
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

// flush releases any buffered text at stream end (an unterminated thinking
// block reports as reasoning; buffered plain text reports as content).
func (s *codexThinkingTagState) flush() (reasoning, content string) {
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

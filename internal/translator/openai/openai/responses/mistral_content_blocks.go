package responses

import (
	"strings"

	"github.com/tidwall/gjson"
)

// splitContentBlocksArray splits a Mistral-style content array into reasoning and
// visible text. When reasoning is enabled, Mistral returns message.content as an
// array of ThinkChunk/TextChunk blocks instead of a plain string or a
// reasoning_content field; without splitting, the raw JSON array leaks into the
// visible output. It returns isArray=false when content is not an array.
func splitContentBlocksArray(content gjson.Result) (reasoning string, visible string, isArray bool) {
	if !content.IsArray() {
		return "", "", false
	}
	var reasoningBuf, visibleBuf strings.Builder
	content.ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "thinking":
			thinking := block.Get("thinking")
			if thinking.IsArray() {
				thinking.ForEach(func(_, chunk gjson.Result) bool {
					reasoningBuf.WriteString(chunk.Get("text").String())
					return true
				})
			} else {
				reasoningBuf.WriteString(thinking.String())
			}
		case "text":
			visibleBuf.WriteString(block.Get("text").String())
		}
		return true
	})
	return reasoningBuf.String(), visibleBuf.String(), true
}

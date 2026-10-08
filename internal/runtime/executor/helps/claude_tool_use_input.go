package helps

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeClaudeToolUseInputs rewrites assistant tool_use.input values that the
// Anthropic Messages API rejects ("Input should be an object") into the object
// form when no meaning is lost:
//   - missing, null, or empty/blank string input becomes {}
//   - a string that holds a JSON object is decoded into that object
//
// Every other value (arrays, numbers, booleans, plain or malformed strings) is
// left untouched so meaningful arguments are never silently replaced by {}.
func NormalizeClaudeToolUseInputs(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	type edit struct {
		path string
		raw  string
	}
	var edits []edit
	messages.ForEach(func(messageIndex, message gjson.Result) bool {
		content := message.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(blockIndex, block gjson.Result) bool {
			if block.Get("type").String() != "tool_use" {
				return true
			}
			if raw, ok := normalizedClaudeToolUseInput(block.Get("input")); ok {
				edits = append(edits, edit{
					path: fmt.Sprintf("messages.%d.content.%d.input", messageIndex.Int(), blockIndex.Int()),
					raw:  raw,
				})
			}
			return true
		})
		return true
	})
	for _, e := range edits {
		if updated, errSet := sjson.SetRawBytes(body, e.path, []byte(e.raw)); errSet == nil {
			body = updated
		}
	}
	return body
}

func normalizedClaudeToolUseInput(input gjson.Result) (string, bool) {
	switch {
	case !input.Exists(), input.Type == gjson.Null:
		return "{}", true
	case input.Type == gjson.String:
		text := strings.TrimSpace(input.String())
		if text == "" {
			return "{}", true
		}
		if gjson.Valid(text) {
			if parsed := gjson.Parse(text); parsed.IsObject() {
				return parsed.Raw, true
			}
		}
	}
	return "", false
}

package helps

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
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
	if len(body) == 0 {
		return body
	}
	doc := ojson.NewDoc(body)
	if !NormalizeClaudeToolUseInputsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

// NormalizeClaudeToolUseInputsDoc is the in-place Doc form of NormalizeClaudeToolUseInputs.
// It reports whether doc changed.
func NormalizeClaudeToolUseInputsDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return false
	}
	type edit struct {
		path string
		raw  string
	}
	var edits []edit
	messages.ForEach(func(messageIndex, message ojson.Result) bool {
		content := message.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(blockIndex, block ojson.Result) bool {
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
	changed := false
	for _, e := range edits {
		if errSet := doc.SetRaw(e.path, e.raw); errSet == nil {
			changed = true
		}
	}
	return changed
}

func normalizedClaudeToolUseInput(input ojson.Result) (string, bool) {
	switch {
	case !input.Exists(), input.Type == ojson.Null:
		return "{}", true
	case input.Type == ojson.String:
		text := strings.TrimSpace(input.String())
		if text == "" {
			return "{}", true
		}
		if ojson.Valid(text) {
			if parsed := ojson.Parse(text); parsed.IsObject() {
				return parsed.Raw, true
			}
		}
	}
	return "", false
}

package helps

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeOpenAISystemContentAsString joins text-only system blocks with newlines.
// Mixed or non-text content is preserved rather than silently discarding blocks.
func NormalizeOpenAISystemContentAsString(payload []byte) ([]byte, error) {
	for index, message := range gjson.GetBytes(payload, "messages").Array() {
		content := message.Get("content")
		if message.Get("role").String() != "system" || !content.IsArray() {
			continue
		}
		parts := content.Array()
		texts := make([]string, 0, len(parts))
		textOnly := true
		for _, part := range parts {
			text := part.Get("text")
			if part.Get("type").String() != "text" || text.Type != gjson.String {
				textOnly = false
				break
			}
			texts = append(texts, text.String())
		}
		if !textOnly {
			continue
		}
		updated, err := sjson.SetBytes(payload, "messages."+strconv.Itoa(index)+".content", strings.Join(texts, "\n"))
		if err != nil {
			return nil, err
		}
		payload = updated
	}
	return payload, nil
}

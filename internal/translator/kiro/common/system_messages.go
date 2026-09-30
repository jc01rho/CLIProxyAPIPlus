package common

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// SplitKiroSystemMessages extracts only the leading system/developer prefix.
// Later instructions stay in conversation order as user reminders, before
// adjacent user turns are merged. Ported from kiro-lb src/convert_openai.rs,
// src/convert_anthropic.rs and src/convert_core.rs (1581af9).
func SplitKiroSystemMessages(messages []gjson.Result) ([]string, []gjson.Result) {
	var system []string
	ordered := make([]gjson.Result, 0, len(messages))
	leading := true
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
		if role != "system" && role != "developer" {
			leading = false
			ordered = append(ordered, message)
			continue
		}
		var parts []string
		content := message.Get("content")
		if content.Type == gjson.String {
			parts = append(parts, content.String())
		} else if content.IsArray() {
			for _, part := range content.Array() {
				if part.Type == gjson.String {
					parts = append(parts, part.String())
				} else if part.Get("type").String() == "text" {
					parts = append(parts, part.Get("text").String())
				}
			}
		}
		text := strings.Join(parts, "\n")
		if text == "" {
			continue
		}
		if leading {
			system = append(system, text)
			continue
		}
		// A string-only map is always JSON encodable.
		raw, _ := json.Marshal(map[string]string{
			"role": "user", "content": "<system-reminder>\n" + text + "\n</system-reminder>",
		})
		ordered = append(ordered, gjson.ParseBytes(raw))
	}
	return system, ordered
}

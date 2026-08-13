package openai

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const cursorFlattenToolResultLimit = 8000

// ConvertOpenAIRequestToCursor converts an OpenAI Chat Completions request into
// the Cursor executor's OpenAI-shaped payload. The executor still parses
// messages/tools itself so structured tool results stay available for H2 session
// resume; this translator only normalizes the model field.
func ConvertOpenAIRequestToCursor(modelName string, inputRawJSON []byte, _ bool) []byte {
	currentModel := gjson.GetBytes(inputRawJSON, "model")
	if currentModel.Type == gjson.String && currentModel.String() == modelName {
		return inputRawJSON
	}
	updatedJSON, err := sjson.SetBytes(inputRawJSON, "model", modelName)
	if err != nil {
		return inputRawJSON
	}
	return updatedJSON
}

// FlattenConversationIntoUserText is the 9router-style cold-resume helper.
// Cursor reliably reads a single UserText blob and often ignores structured
// turns, so prior user/assistant turns and tool results are folded into the
// latest user message. The executor calls this only when no H2 checkpoint or
// live session exists; applying it unconditionally would break tool resume.
func FlattenConversationIntoUserText(rawJSON []byte) []byte {
	messages := gjson.GetBytes(rawJSON, "messages")
	if !messages.IsArray() {
		return rawJSON
	}

	var (
		buf         strings.Builder
		pendingUser string
		userText    string
		hasHistory  bool
	)

	for _, msg := range messages.Array() {
		switch msg.Get("role").String() {
		case "system":
			continue
		case "tool":
			hasHistory = true
			content := extractTextContent(msg.Get("content"))
			if len(content) > cursorFlattenToolResultLimit {
				content = content[:cursorFlattenToolResultLimit] + "\n... [truncated]"
			}
			buf.WriteString("TOOL_RESULT (call_id: ")
			buf.WriteString(msg.Get("tool_call_id").String())
			buf.WriteString("): ")
			buf.WriteString(content)
			buf.WriteString("\n\n")
		case "user":
			if pendingUser != "" {
				hasHistory = true
				buf.WriteString("USER: ")
				buf.WriteString(pendingUser)
				buf.WriteString("\n\n")
			}
			pendingUser = extractTextContent(msg.Get("content"))
		case "assistant":
			assistantText := extractTextContent(msg.Get("content"))
			if pendingUser != "" {
				hasHistory = true
				buf.WriteString("USER: ")
				buf.WriteString(pendingUser)
				buf.WriteString("\n\n")
				if assistantText != "" {
					buf.WriteString("ASSISTANT: ")
					buf.WriteString(assistantText)
					buf.WriteString("\n\n")
				}
				pendingUser = ""
			} else if assistantText != "" {
				hasHistory = true
				buf.WriteString("ASSISTANT: ")
				buf.WriteString(assistantText)
				buf.WriteString("\n\n")
			}
		}
	}

	if pendingUser != "" {
		userText = pendingUser
	}

	if buf.Len() == 0 && userText == "" {
		return rawJSON
	}
	if hasHistory {
		buf.WriteString("The above is the previous conversation context including tool call results.\n")
		buf.WriteString("Continue your response based on this context.\n\n")
	}
	if userText != "" {
		if buf.Len() > 0 {
			userText = buf.String() + "Current request: " + userText
		}
	} else {
		userText = buf.String() + "Continue from the conversation above."
	}

	flattened := []byte(`[]`)
	systemParts := make([]string, 0)
	for _, msg := range messages.Array() {
		if msg.Get("role").String() == "system" {
			systemParts = append(systemParts, extractTextContent(msg.Get("content")))
		}
	}
	if len(systemParts) > 0 {
		updated, err := sjson.SetBytes(flattened, "-1", map[string]any{
			"role":    "system",
			"content": strings.Join(systemParts, "\n"),
		})
		if err == nil {
			flattened = updated
		}
	}
	updated, err := sjson.SetBytes(flattened, "-1", map[string]any{
		"role":    "user",
		"content": userText,
	})
	if err != nil {
		return rawJSON
	}
	out, err := sjson.SetRawBytes(rawJSON, "messages", updated)
	if err != nil {
		return rawJSON
	}
	return out
}

func extractTextContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var parts []string
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				parts = append(parts, part.Get("text").String())
			}
		}
		return strings.Join(parts, "")
	}
	return content.String()
}

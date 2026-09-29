package helps

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

// ResponsesEventReader reads complete SSE frames, not individual data lines.
// Event names are retained for servers that omit the JSON type discriminator.
type ResponsesEventReader struct {
	scanner *bufio.Scanner
}

func NewResponsesEventReader(body io.Reader) *ResponsesEventReader {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 50<<20)
	return &ResponsesEventReader{scanner: scanner}
}

func (r *ResponsesEventReader) Next() (event string, data []byte, err error) {
	var lines [][]byte
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if len(line) == 0 {
			if len(lines) > 0 {
				return event, bytes.Join(lines, []byte("\n")), nil
			}
			event = ""
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := strings.Cut(string(line), ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			lines = append(lines, []byte(value))
		default:
			// JSON errors are sometimes returned with an SSE content type.
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) {
				return "", nil, fmt.Errorf("expected Responses SSE frame, received bare JSON")
			}
		}
	}
	if err = r.scanner.Err(); err != nil {
		return "", nil, err
	}
	// SSE dispatch requires a blank line. A partial frame at EOF is not terminal.
	return "", nil, io.EOF
}

// IsResponsesMeaningfulOutputEvent reports whether the given parsed event
// type + data represent the first meaningful upstream output. A bare
// output_item.added with an empty content array / no function-call id is
// scaffolding, not content.
func IsResponsesMeaningfulOutputEvent(eventType string, data []byte) bool {
	switch eventType {
	case "response.output_text.delta",
		"response.reasoning_text.delta",
		"response.reasoning.delta",
		"response.refusal.delta",
		"response.function_call_arguments.delta",
		"response.custom_tool_call_input.delta",
		"response.code_interpreter_call_code.delta",
		"response.mcp_call_arguments.delta",
		"response.shell_call_command.delta",
		"response.audio.transcript.delta",
		"response.reasoning_summary_text.delta":
		return gjson.GetBytes(data, "delta").String() != ""
	case "response.output_text.done",
		"response.reasoning_text.done",
		"response.reasoning_summary_text.done",
		"response.refusal.done",
		"response.function_call_arguments.done",
		"response.custom_tool_call_input.done",
		"response.code_interpreter_call_code.done",
		"response.shell_call_command.done",
		"response.mcp_call_arguments.done":
		return gjson.GetBytes(data, "text").String() != "" || gjson.GetBytes(data, "arguments").String() != "" || gjson.GetBytes(data, "input").String() != "" || gjson.GetBytes(data, "refusal").String() != ""
	case "response.output_item.added", "response.output_item.done":
		itemType := gjson.GetBytes(data, "item.type").String()
		itemID := strings.TrimSpace(gjson.GetBytes(data, "item.id").String())
		switch itemType {
		case "function_call":
			return itemID != "" && strings.TrimSpace(gjson.GetBytes(data, "item.name").String()) != ""
		case "custom_tool_call":
			return itemID != "" && strings.TrimSpace(gjson.GetBytes(data, "item.name").String()) != ""
		case "message":
			for _, c := range gjson.GetBytes(data, "item.content").Array() {
				if c.Get("text").String() != "" || c.Get("refusal").String() != "" {
					return true
				}
			}
			return false
		default:
			return false
		}
	case "response.content_part.added", "response.content_part.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return gjson.GetBytes(data, "part.text").String() != "" || gjson.GetBytes(data, "part.refusal").String() != ""
	case "response.completed", "response.incomplete":
		return true
	default:
		return false
	}
}

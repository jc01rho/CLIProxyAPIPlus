// commandcode_request_convert.go holds the message/tool conversion helpers for
// the CommandCode request wire conversion defined in commandcode_request.go.

package executor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// commandCodeMessageContent splits the OpenAI content union (plain string or
// structured part array) into typed values at the boundary.
func commandCodeMessageContent(raw json.RawMessage) (string, []commandCodeOpenAIContentPart, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, nil
	}
	switch raw[0] {
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", nil, fmt.Errorf("commandcode: message content: %w", err)
		}
		return text, nil, nil
	case '[':
		var parts []commandCodeOpenAIContentPart
		if err := json.Unmarshal(raw, &parts); err != nil {
			return "", nil, fmt.Errorf("commandcode: message content parts: %w", err)
		}
		return "", parts, nil
	default:
		return "", nil, fmt.Errorf("commandcode: unsupported message content shape %s", raw)
	}
}

func commandCodeUserMessage(msg commandCodeOpenAIMessage) (commandCodeWireMessage, error) {
	text, parts, err := commandCodeMessageContent(msg.Content)
	if err != nil {
		return commandCodeWireMessage{}, err
	}
	blocks := make([]commandCodeWireContentBlock, 0, len(parts)+1)
	if text != "" {
		blocks = append(blocks, commandCodeWireTextBlock{Type: "text", Text: text})
	}
	for _, part := range parts {
		switch part.Type {
		case "text":
			blocks = append(blocks, commandCodeWireTextBlock{Type: "text", Text: part.Text})
		case "image_url":
			if part.ImageURL == nil || part.ImageURL.URL == "" {
				return commandCodeWireMessage{}, fmt.Errorf("commandcode: image_url part without url")
			}
			url := part.ImageURL.URL
			mime, ok := strings.CutPrefix(url, "data:")
			if !ok {
				return commandCodeWireMessage{}, fmt.Errorf("commandcode: unsupported non-data image url")
			}
			mime, _, _ = strings.Cut(mime, ";")
			if mime == "" {
				return commandCodeWireMessage{}, fmt.Errorf("commandcode: image data url without mime type")
			}
			blocks = append(blocks, commandCodeWireImageBlock{Type: "image", Image: url, MimeType: mime})
		default:
			return commandCodeWireMessage{}, fmt.Errorf("commandcode: unsupported user content part type %q", part.Type)
		}
	}
	return commandCodeWireMessage{Role: "user", Content: blocks}, nil
}

func commandCodeAssistantMessage(msg commandCodeOpenAIMessage) (commandCodeWireMessage, error) {
	text, parts, err := commandCodeMessageContent(msg.Content)
	if err != nil {
		return commandCodeWireMessage{}, err
	}
	blocks := make([]commandCodeWireContentBlock, 0, len(parts)+len(msg.ToolCalls)+1)
	if text != "" {
		blocks = append(blocks, commandCodeWireTextBlock{Type: "text", Text: text})
	}
	for _, part := range parts {
		switch part.Type {
		case "text":
			blocks = append(blocks, commandCodeWireTextBlock{Type: "text", Text: part.Text})
		case "reasoning":
			blocks = append(blocks, commandCodeWireTextBlock{Type: "reasoning", Text: part.Text})
		default:
			return commandCodeWireMessage{}, fmt.Errorf("commandcode: unsupported assistant content part type %q", part.Type)
		}
	}
	for _, call := range msg.ToolCalls {
		if call.ID == "" {
			return commandCodeWireMessage{}, fmt.Errorf("commandcode: invalid tool call: id=%q name=%q", call.ID, call.Function.Name)
		}
		if call.Function.Name == "" {
			continue
		}
		input, err := commandCodeToolCallInput(call)
		if err != nil {
			return commandCodeWireMessage{}, err
		}
		blocks = append(blocks, commandCodeWireToolCallBlock{
			Type:       "tool-call",
			ToolCallID: call.ID,
			ToolName:   call.Function.Name,
			Input:      input,
		})
	}
	return commandCodeWireMessage{Role: "assistant", Content: blocks}, nil
}

// commandCodeToolCallInput parses the OpenAI arguments string into the raw JSON
// object the wire tool-call block carries.
func commandCodeToolCallInput(call commandCodeOpenAIToolCall) (json.RawMessage, error) {
	raw := call.Function.Arguments
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return nil, fmt.Errorf("commandcode: tool call %q arguments: %w", call.ID, err)
		}
		raw = json.RawMessage(strings.TrimSpace(encoded))
		if len(raw) == 0 {
			return json.RawMessage(`{}`), nil
		}
	}
	if raw[0] != '{' || !json.Valid(raw) {
		return nil, fmt.Errorf("commandcode: tool call %q arguments are not a JSON object", call.ID)
	}
	return raw, nil
}

func commandCodeToolMessage(msg commandCodeOpenAIMessage, toolNames map[string]string) (commandCodeWireMessage, error) {
	if msg.ToolCallID == "" {
		return commandCodeWireMessage{}, fmt.Errorf("commandcode: tool message without tool_call_id")
	}
	text, err := commandCodeTextContent(msg)
	if err != nil {
		return commandCodeWireMessage{}, err
	}
	return commandCodeWireMessage{
		Role: "tool",
		Content: []commandCodeWireContentBlock{commandCodeWireToolResultBlock{
			Type:       "tool-result",
			ToolCallID: msg.ToolCallID,
			ToolName:   toolNames[msg.ToolCallID],
			Output:     commandCodeWireToolOutput{Type: "text", Value: text},
		}},
	}, nil
}

// commandCodeTextContent flattens text-only content (system/developer/tool
// messages) into a single string; non-text parts fail.
func commandCodeTextContent(msg commandCodeOpenAIMessage) (string, error) {
	text, parts, err := commandCodeMessageContent(msg.Content)
	if err != nil {
		return "", err
	}
	if parts == nil {
		return text, nil
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type != "text" {
			return "", fmt.Errorf("commandcode: unsupported %s content part type %q", msg.Role, part.Type)
		}
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, "\n"), nil
}

// commandCodeConvertTools converts OpenAI function tools to the CommandCode
// {name, description, input_schema} wire shape without the type:"function" wrapper.
func commandCodeConvertTools(tools []commandCodeOpenAITool) []commandCodeWireTool {
	if len(tools) == 0 {
		return nil
	}
	wire := make([]commandCodeWireTool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Function.Parameters
		if len(parameters) == 0 || string(parameters) == "null" {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		wire = append(wire, commandCodeWireTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: parameters,
		})
	}
	return wire
}

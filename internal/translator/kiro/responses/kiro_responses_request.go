package responses

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	kiroopenai "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/kiro/openai"
	"github.com/tidwall/gjson"
)

// RequestError is a client error, not an upstream or credential failure.
type RequestError struct{ Message string }

func (e *RequestError) Error() string   { return e.Message }
func (e *RequestError) StatusCode() int { return http.StatusBadRequest }

// ConvertOpenAIResponsesRequestToKiro preserves the source until the executor
// supplies the profile and endpoint origin, like the existing Kiro chat adapter.
// Its error-capable payload builder performs validation before generation.
func ConvertOpenAIResponsesRequestToKiro(_ string, body []byte, _ bool) []byte {
	return body
}

// BuildKiroPayloadFromResponses uses the same chat pipeline as kiro-lb's facade.
// Ported from kiro-lb src/convert_responses.rs (1581af9).
func BuildKiroPayloadFromResponses(body []byte, model, profileARN, origin string, agentic, chatOnly bool, headers http.Header) ([]byte, bool, error) {
	chat, err := ResponsesRequestToChat(body)
	if err != nil {
		return nil, false, err
	}
	payload, thinking := kiroopenai.BuildKiroPayloadFromOpenAI(chat, model, profileARN, origin, agentic, chatOnly, headers, nil)
	return payload, thinking, nil
}

// ResponsesRequestToChat converts the stateless Responses conversation to chat.
// Reasoning items are deliberately not replayed upstream.
func ResponsesRequestToChat(body []byte) ([]byte, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, &RequestError{Message: "Kiro Responses request must be a JSON object"}
	}
	r := gjson.ParseBytes(body)
	if previous := r.Get("previous_response_id"); previous.Exists() && previous.Type != gjson.Null && previous.String() != "" {
		return nil, &RequestError{Message: "previous_response_id is not supported: Kiro stores no responses. Send the full conversation in input with store=false."}
	}
	messages := make([]any, 0)
	if instructions := r.Get("instructions"); instructions.String() != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions.Value()})
	}
	input := r.Get("input")
	if input.Type == gjson.String && input.String() != "" {
		messages = append(messages, map[string]any{"role": "user", "content": input.String()})
	}
	for _, item := range input.Array() {
		kind := item.Get("type").String()
		if kind == "" && item.Get("role").String() != "" {
			kind = "message"
		}
		switch kind {
		case "message", "input_text", "output_text":
			content := item.Get("content")
			if !content.Exists() {
				content = item.Get("text")
			}
			text, images := contentToText(content)
			role := item.Get("role").String()
			if role == "" {
				role = "user"
			}
			if len(images) > 0 {
				parts := make([]any, 0, len(images)+1)
				if text != "" {
					parts = append(parts, map[string]any{"type": "text", "text": text})
				}
				parts = append(parts, images...)
				messages = append(messages, map[string]any{"role": role, "content": parts})
			} else if text != "" {
				messages = append(messages, map[string]any{"role": role, "content": text})
			}
		case "function_call", "custom_tool_call":
			args := item.Get("arguments").String()
			if kind == "custom_tool_call" {
				args = string(encodeJSON(map[string]any{"input": item.Get("input").String()}))
			} else if args == "" {
				args = "{}"
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
				map[string]any{"id": callID(item, true), "type": "function", "function": map[string]any{"name": item.Get("name").String(), "arguments": args}},
			}})
		case "function_call_output", "custom_tool_call_output":
			output := item.Get("output")
			if output.IsObject() {
				for _, key := range []string{"content", "text"} {
					if value := output.Get(key); value.Type == gjson.String && value.String() != "" {
						output = value
						break
					}
				}
			}
			text, _ := contentToText(output)
			messages = append(messages, map[string]any{"role": "tool", "content": text, "tool_call_id": callID(item, false)})
		}
	}
	if len(messages) == 0 {
		return nil, &RequestError{Message: "input must contain at least one message"}
	}
	chat := map[string]any{"model": r.Get("model").String(), "messages": messages, "stream": r.Get("stream").Bool()}
	tools := make([]any, 0)
	for _, tool := range declaredTools(r) {
		kind := strings.ToLower(tool.Get("type").String())
		name := tool.Get("name").String()
		if name == "" {
			name = tool.Get("function.name").String()
		}
		if name == "" {
			continue
		}
		fn := map[string]any{"name": name}
		switch kind {
		case "custom":
			description := strings.TrimRight(tool.Get("description").String(), " \n\t") + "\n\nSupply the complete tool body verbatim in the single string field input. Do not add markdown fences or split the body into other fields."
			if syntax := tool.Get("format.syntax").String(); syntax != "" {
				description += " The body is " + syntax + " source."
			}
			fn["description"] = description
			fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}}, "required": []string{"input"}}
		case "", "function":
			for _, key := range []string{"description", "parameters"} {
				value := tool.Get(key)
				if !value.Exists() || value.Type == gjson.Null {
					value = tool.Get("function." + key)
				}
				fn[key] = value.Value()
			}
		default:
			continue
		}
		tools = append(tools, map[string]any{"type": "function", "function": fn})
	}
	if len(tools) > 0 {
		chat["tools"] = tools
	}
	if choice := r.Get("tool_choice"); choice.Exists() {
		name := choice.Get("name").String()
		if name == "" {
			name = choice.Get("function.name").String()
		}
		if name != "" && (choice.Get("type").String() == "function" || choice.Get("type").String() == "custom") {
			chat["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": name}}
		} else {
			chat["tool_choice"] = choice.Value()
		}
	}
	for _, key := range []string{"parallel_tool_calls", "temperature", "top_p"} {
		if value := r.Get(key); value.Exists() && value.Type != gjson.Null {
			chat[key] = value.Value()
		}
	}
	if value := r.Get("max_output_tokens"); value.Exists() {
		chat["max_tokens"] = value.Value()
	}
	if effort := normalizeEffort(r.Get("reasoning.effort").String()); effort != "" {
		chat["reasoning_effort"] = effort
	}
	return json.Marshal(chat)
}

func normalizeEffort(effort string) string {
	switch effort = strings.ToLower(strings.TrimSpace(effort)); effort {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return effort
	case "off", "disabled", "0":
		return "none"
	case "ultra", "persistent":
		return "max"
	default:
		return ""
	}
}

func contentToText(content gjson.Result) (string, []any) {
	if content.Type == gjson.Null {
		return "", nil
	}
	if !content.IsArray() {
		return content.String(), nil
	}
	var texts []string
	var images []any
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "input_image", "image_url", "image":
			url := part.Get("image_url")
			if url.IsObject() {
				url = url.Get("url")
			}
			if url.String() != "" {
				images = append(images, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url.String()}})
			}
		default:
			if text := part.Get("text").String(); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n"), images
}

func declaredTools(request gjson.Result) []gjson.Result {
	var tools []gjson.Result
	var flatten func(gjson.Result)
	flatten = func(entries gjson.Result) {
		for _, entry := range entries.Array() {
			if strings.EqualFold(entry.Get("type").String(), "namespace") {
				flatten(entry.Get("tools"))
			} else {
				tools = append(tools, entry)
			}
		}
	}
	flatten(request.Get("tools"))
	for _, item := range request.Get("input").Array() {
		if item.Get("type").String() == "additional_tools" {
			flatten(item.Get("tools"))
		}
	}
	return tools
}

func callID(item gjson.Result, generate bool) string {
	if id := item.Get("call_id").String(); id != "" {
		return id
	}
	if id := item.Get("id").String(); id != "" {
		return id
	}
	if generate {
		return newID("call")
	}
	return ""
}

func newID(prefix string) string { return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "") }

// All callers supply JSON-derived values or concrete JSON-compatible fields.
func encodeJSON(value any) []byte {
	body, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Errorf("encode Kiro Responses value: %w", err))
	}
	return body
}

package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

type codexRequestFormatError struct {
	statusErr
}

func (e *codexRequestFormatError) IsRequestScoped() bool { return e != nil }

func rejectInvalidCodexRequestFormat(body []byte) error {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	return rejectCodexStructuredOutputUniqueItems(body)
}

func rejectCodexStructuredOutputUniqueItems(body []byte) error {
	format := gjson.GetBytes(body, "text.format")
	if format.IsObject() {
		schema := format.Get("schema")
		if schema.IsObject() {
			if path, ok := findJSONSchemaUniqueItems(schema.Value()); ok {
				name := strings.TrimSpace(format.Get("name").String())
				if name == "" {
					name = "response"
				}
				return newCodexStructuredOutputError(
					fmt.Sprintf("Invalid schema for response_format '%s': In context=%s, 'uniqueItems' is not permitted.", name, formatSchemaContext(path)),
					"text.format.schema",
				)
			}
		}
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return nil
	}
	for i, tool := range tools.Array() {
		schema := tool.Get("parameters")
		param := fmt.Sprintf("tools.%d.parameters", i)
		if !schema.IsObject() {
			schema = tool.Get("function.parameters")
			param = fmt.Sprintf("tools.%d.function.parameters", i)
		}
		if !schema.IsObject() {
			continue
		}
		path, ok := findJSONSchemaUniqueItems(schema.Value())
		if !ok {
			continue
		}
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("function.name").String())
		}
		if name == "" {
			name = fmt.Sprintf("tools[%d]", i)
		}
		return newCodexStructuredOutputError(
			fmt.Sprintf("Invalid schema for tool '%s': In context=%s, 'uniqueItems' is not permitted.", name, formatSchemaContext(path)),
			param,
		)
	}
	return nil
}

func newCodexStructuredOutputError(message, param string) error {
	payload, errMarshal := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"param":   param,
			"code":    "invalid_json_schema",
		},
	})
	if errMarshal != nil {
		payload = []byte(`{"error":{"message":"uniqueItems is not permitted","type":"invalid_request_error","code":"invalid_json_schema"}}`)
	}
	return &codexRequestFormatError{statusErr: statusErr{code: http.StatusBadRequest, msg: string(payload)}}
}

func formatSchemaContext(path []string) string {
	if len(path) == 0 {
		return "()"
	}
	quoted := make([]string, len(path))
	for i, part := range path {
		quoted[i] = "'" + part + "'"
	}
	return "(" + strings.Join(quoted, ", ") + ")"
}

func findJSONSchemaUniqueItems(node any) ([]string, bool) {
	switch typed := node.(type) {
	case map[string]any:
		if _, exists := typed["uniqueItems"]; exists {
			return nil, true
		}
		for _, key := range []string{"items", "not", "if", "then", "else", "contains", "additionalProperties", "unevaluatedProperties", "additionalItems"} {
			child, exists := typed[key]
			if !exists {
				continue
			}
			if path, found := findJSONSchemaUniqueItems(child); found {
				return append([]string{key}, path...), true
			}
		}
		for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
			child, exists := typed[key]
			if !exists {
				continue
			}
			if path, found := findJSONSchemaUniqueItems(child); found {
				return append([]string{key}, path...), true
			}
		}
		for _, key := range []string{"properties", "$defs", "definitions", "patternProperties", "dependentSchemas"} {
			child, exists := typed[key]
			obj, ok := child.(map[string]any)
			if !exists || !ok {
				continue
			}
			for name, schema := range obj {
				if path, found := findJSONSchemaUniqueItems(schema); found {
					return append([]string{key, name}, path...), true
				}
			}
		}
	case []any:
		for i, child := range typed {
			if path, found := findJSONSchemaUniqueItems(child); found {
				return append([]string{strconv.Itoa(i)}, path...), true
			}
		}
	}
	return nil, false
}

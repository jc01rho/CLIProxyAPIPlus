package proto

import (
	"encoding/json"
	"testing"
)

// TestSanitizeCursorToolSchemaStripsCompositionKeys confirms the Cursor tool
// input-schema sanitizer removes oneOf/anyOf/allOf while preserving every
// other key, including "not" and nested unmodified subtrees.
func TestSanitizeCursorToolSchemaStripsCompositionKeys(t *testing.T) {
	input := []byte(`{
		"type": "object",
		"oneOf": [{"required": ["a"]}, {"required": ["b"]}],
		"anyOf": [{"type": "string"}],
		"allOf": [{"required": ["c"]}],
		"not": {"type": "null"},
		"properties": {
			"value": {
				"type": "string",
				"oneOf": [{"const": "x"}],
				"not": {"enum": ["y"]}
			}
		},
		"required": ["value"],
		"additionalProperties": false
	}`)

	got := SanitizeCursorToolSchema(input)

	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("sanitized output is not valid JSON: %v; output=%s", err, got)
	}

	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if _, ok := obj[key]; ok {
			t.Errorf("%q must be stripped, still present in: %s", key, got)
		}
	}

	if _, ok := obj["not"]; !ok {
		t.Errorf("\"not\" must be preserved; missing from: %s", got)
	}
	if _, ok := obj["type"]; !ok {
		t.Errorf("\"type\" must be preserved; missing from: %s", got)
	}
	if _, ok := obj["required"]; !ok {
		t.Errorf("\"required\" must be preserved; missing from: %s", got)
	}

	props, ok := obj["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties not preserved as object: %s", got)
	}
	value, ok := props["value"].(map[string]any)
	if !ok {
		t.Fatalf("properties.value not preserved as object: %s", got)
	}
	if _, ok := value["oneOf"]; ok {
		t.Errorf("nested oneOf must be stripped, still present in: %s", got)
	}
	if _, ok := value["not"]; !ok {
		t.Errorf("nested \"not\" must be preserved; missing from: %s", got)
	}
}

// TestSanitizeCursorToolSchemaPreservesNonObjectInputs confirms that nil,
// empty, invalid JSON, and non-object JSON are returned unchanged.
func TestSanitizeCursorToolSchemaPreservesNonObjectInputs(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"invalid JSON", []byte(`{"type": object broken`)},
		{"JSON string", []byte(`"hello"`)},
		{"JSON number", []byte(`42`)},
		{"JSON array", []byte(`["oneOf"]`)},
		{"JSON null", []byte(`null`)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SanitizeCursorToolSchema(c.input)
			if string(got) != string(c.input) {
				t.Errorf("SanitizeCursorToolSchema(%q) = %q, want unchanged %q", c.input, got, c.input)
			}
		})
	}
}

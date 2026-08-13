package executor

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripKimiUnsupportedFields(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantRemoved   []string
		wantPreserved []string
	}{
		{
			name:          "no unsupported fields",
			input:         `{"model":"moonshot-v1-8k","messages":[{"role":"user","content":"hi"}]}`,
			wantPreserved: []string{"model", "messages"},
		},
		{
			name:          "interleaved removed",
			input:         `{"model":"moonshot-v1-8k","interleaved":true}`,
			wantRemoved:   []string{"interleaved"},
			wantPreserved: []string{"model"},
		},
		{
			name:          "reasoning removed",
			input:         `{"model":"moonshot-v1-8k","reasoning":{"effort":"high"}}`,
			wantRemoved:   []string{"reasoning"},
			wantPreserved: []string{"model"},
		},
		{
			name:          "reasoning_effort removed",
			input:         `{"model":"moonshot-v1-8k","reasoning_effort":"high"}`,
			wantRemoved:   []string{"reasoning_effort"},
			wantPreserved: []string{"model"},
		},
		{
			name:          "reasoningSummary removed",
			input:         `{"model":"moonshot-v1-8k","reasoningSummary":"concise"}`,
			wantRemoved:   []string{"reasoningSummary"},
			wantPreserved: []string{"model"},
		},
		{
			name:          "include removed",
			input:         `{"model":"moonshot-v1-8k","include":["reasoning"]}`,
			wantRemoved:   []string{"include"},
			wantPreserved: []string{"model"},
		},
		{
			name:          "verbosity removed",
			input:         `{"model":"moonshot-v1-8k","verbosity":"verbose"}`,
			wantRemoved:   []string{"verbosity"},
			wantPreserved: []string{"model"},
		},
		{
			name:          "multiple unsupported fields removed",
			input:         `{"model":"moonshot-v1-8k","messages":[],"interleaved":true,"reasoning":{},"reasoning_effort":"medium","reasoningSummary":"auto","include":[],"verbosity":"quiet"}`,
			wantRemoved:   []string{"interleaved", "reasoning", "reasoning_effort", "reasoningSummary", "include", "verbosity"},
			wantPreserved: []string{"model", "messages"},
		},
		{
			name:          "supported fields preserved",
			input:         `{"model":"moonshot-v1-8k","messages":[{"role":"user","content":"hello"}],"temperature":0.7,"max_tokens":100}`,
			wantPreserved: []string{"model", "messages", "temperature", "max_tokens"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripKimiUnsupportedFields([]byte(tt.input))

			var m map[string]interface{}
			if err := json.Unmarshal(result, &m); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}

			for _, key := range tt.wantRemoved {
				if _, ok := m[key]; ok {
					t.Errorf("field %q should have been removed but is still present", key)
				}
			}

			for _, key := range tt.wantPreserved {
				if _, ok := m[key]; !ok {
					t.Errorf("field %q should be preserved but is missing", key)
				}
			}
		})
	}
}

func TestNormalizeKimiMessageRoles(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantRoles []string
	}{
		{
			name:      "leading developer becomes system",
			input:     `{"messages":[{"role":"developer","content":"policy"},{"role":"user","content":"hi"}]}`,
			wantRoles: []string{"system", "user"},
		},
		{
			name:      "developer after conversation becomes system",
			input:     `{"messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"},{"role":"developer","content":"late"},{"role":"assistant","content":"ok"}]}`,
			wantRoles: []string{"system", "user", "system", "assistant"},
		},
		{
			name:      "mixed case developer normalized",
			input:     `{"messages":[{"role":" Developer ","content":"policy"}]}`,
			wantRoles: []string{"system"},
		},
		{
			name:      "other roles untouched",
			input:     `{"messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`,
			wantRoles: []string{"user", "tool"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := normalizeKimiMessageRoles([]byte(tt.input))
			roles := gjson.GetBytes(out, "messages.#.role").Array()
			if len(roles) != len(tt.wantRoles) {
				t.Fatalf("roles length = %d, want %d; output=%s", len(roles), len(tt.wantRoles), out)
			}
			for i, want := range tt.wantRoles {
				if got := roles[i].String(); got != want {
					t.Fatalf("messages.%d.role = %q, want %q; output=%s", i, got, want, out)
				}
			}
		})
	}
}

func TestNormalizeKimiMessageRolesPreservesContentAndInvalidPayloads(t *testing.T) {
	input := []byte(`{"model":"k3","messages":[{"role":"developer","content":[{"type":"text","text":"rule"}],"name":"ops"}]}`)
	out := normalizeKimiMessageRoles(input)
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "rule" {
		t.Fatalf("content text = %q, want rule; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.name").String(); got != "ops" {
		t.Fatalf("name = %q, want ops; output=%s", got, out)
	}

	invalid := []byte(`{not json`)
	if got := normalizeKimiMessageRoles(invalid); string(got) != string(invalid) {
		t.Fatalf("invalid payload rewritten: %s", got)
	}
	if got := normalizeKimiMessageRoles(nil); got != nil {
		t.Fatalf("nil payload rewritten: %s", got)
	}
}

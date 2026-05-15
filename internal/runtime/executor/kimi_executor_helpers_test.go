package executor

import (
	"encoding/json"
	"testing"
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
			name:        "interleaved removed",
			input:       `{"model":"moonshot-v1-8k","interleaved":true}`,
			wantRemoved: []string{"interleaved"},
			wantPreserved: []string{"model"},
		},
		{
			name:        "reasoning removed",
			input:       `{"model":"moonshot-v1-8k","reasoning":{"effort":"high"}}`,
			wantRemoved: []string{"reasoning"},
			wantPreserved: []string{"model"},
		},
		{
			name:        "reasoning_effort removed",
			input:       `{"model":"moonshot-v1-8k","reasoning_effort":"high"}`,
			wantRemoved: []string{"reasoning_effort"},
			wantPreserved: []string{"model"},
		},
		{
			name:        "reasoningSummary removed",
			input:       `{"model":"moonshot-v1-8k","reasoningSummary":"concise"}`,
			wantRemoved: []string{"reasoningSummary"},
			wantPreserved: []string{"model"},
		},
		{
			name:        "include removed",
			input:       `{"model":"moonshot-v1-8k","include":["reasoning"]}`,
			wantRemoved: []string{"include"},
			wantPreserved: []string{"model"},
		},
		{
			name:        "verbosity removed",
			input:       `{"model":"moonshot-v1-8k","verbosity":"verbose"}`,
			wantRemoved: []string{"verbosity"},
			wantPreserved: []string{"model"},
		},
		{
			name: "multiple unsupported fields removed",
			input: `{"model":"moonshot-v1-8k","messages":[],"interleaved":true,"reasoning":{},"reasoning_effort":"medium","reasoningSummary":"auto","include":[],"verbosity":"quiet"}`,
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

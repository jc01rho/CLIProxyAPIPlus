package executor

import (
	"encoding/json"
	"fmt"
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

func TestStripKimiFixedSamplingFields(t *testing.T) {
	fullyFixed := []string{"temperature", "top_p", "presence_penalty", "frequency_penalty", "n"}
	payload := `{"model":"%s","messages":[{"role":"user","content":"hi"}],"temperature":0.5,"top_p":0.9,"presence_penalty":0.1,"frequency_penalty":0.2,"n":2,"max_tokens":64}`

	tests := []struct {
		name          string
		model         string
		wantRemoved   []string
		wantPreserved []string
	}{
		{name: "k3 strips all fixed fields", model: "k3", wantRemoved: fullyFixed, wantPreserved: []string{"model", "messages", "max_tokens"}},
		{name: "kimi-k3 with context and thinking suffix", model: "kimi-k3[1m](high)", wantRemoved: fullyFixed},
		{name: "k3-256k variant", model: "k3-256k", wantRemoved: fullyFixed},
		{name: "k2.6 strips all fixed fields", model: "kimi-k2.6", wantRemoved: fullyFixed},
		{name: "k2.7-code strips all fixed fields", model: "kimi-k2.7-code", wantRemoved: fullyFixed},
		{name: "k2.7-code highspeed strips all fixed fields", model: "k2.7-code-highspeed", wantRemoved: fullyFixed},
		{name: "normalized kimi-for-coding strips all fixed fields", model: "kimi-for-coding", wantRemoved: fullyFixed},
		{name: "k2.5 strips only temperature", model: "kimi-k2.5", wantRemoved: []string{"temperature"}, wantPreserved: []string{"top_p", "presence_penalty", "frequency_penalty", "n"}},
		{name: "k2-thinking strips only temperature", model: "kimi-k2-thinking", wantRemoved: []string{"temperature"}, wantPreserved: []string{"top_p"}},
		{name: "moonshot-v1 untouched", model: "moonshot-v1-8k", wantRemoved: nil, wantPreserved: append([]string{"model", "messages", "max_tokens"}, fullyFixed...)},
		{name: "empty model untouched", model: "", wantRemoved: nil, wantPreserved: append([]string{"model", "messages"}, fullyFixed...)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := stripKimiFixedSamplingFields([]byte(fmt.Sprintf(payload, tt.model)), tt.model)
			for _, field := range tt.wantRemoved {
				if gjson.GetBytes(out, field).Exists() {
					t.Errorf("field %q should be removed for model %q; output=%s", field, tt.model, out)
				}
			}
			for _, field := range tt.wantPreserved {
				if !gjson.GetBytes(out, field).Exists() {
					t.Errorf("field %q should be preserved for model %q; output=%s", field, tt.model, out)
				}
			}
		})
	}
}

func TestStripKimiFixedSamplingFieldsEdgeCases(t *testing.T) {
	// Absent fields leave the payload byte-identical.
	in := []byte(`{"model":"k3","messages":[]}`)
	if out := stripKimiFixedSamplingFields(in, "k3"); string(out) != string(in) {
		t.Fatalf("payload without fixed fields rewritten: %s", out)
	}

	// Explicit temperature=1 is dropped too; upstream applies the same default.
	out := stripKimiFixedSamplingFields([]byte(`{"model":"k3","temperature":1}`), "k3")
	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature=1 should be dropped for k3; output=%s", out)
	}

	invalid := []byte(`{not json`)
	if got := stripKimiFixedSamplingFields(invalid, "k3"); string(got) != string(invalid) {
		t.Fatalf("invalid payload rewritten: %s", got)
	}
	if got := stripKimiFixedSamplingFields(nil, "k3"); got != nil {
		t.Fatalf("nil payload rewritten: %s", got)
	}
}

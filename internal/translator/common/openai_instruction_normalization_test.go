package common

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeLeadingOpenAIInstructions(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantChange bool
		wantHad    bool
		wantRemove bool
		wantReason InstructionNormalizationReason
		wantRoles  []string
		wantText   string
	}{
		{name: "developer only", payload: `{"messages":[{"role":"developer","content":"D"}]}`, wantChange: true, wantHad: true, wantRemove: true, wantReason: InstructionNormalizationReasonNormalized, wantRoles: []string{"system"}, wantText: "D"},
		{name: "system then developer", payload: `{"messages":[{"role":"system","content":"S"},{"role":"developer","content":"D"},{"role":"user","content":"U"}]}`, wantChange: true, wantHad: true, wantRemove: true, wantReason: InstructionNormalizationReasonNormalized, wantRoles: []string{"system", "user"}, wantText: "S\n\nD"},
		{name: "developer then system", payload: `{"messages":[{"role":"developer","content":"D"},{"role":"system","content":"S"},{"role":"user","content":"U"}]}`, wantChange: true, wantHad: true, wantRemove: true, wantReason: InstructionNormalizationReasonNormalized, wantRoles: []string{"system", "user"}, wantText: "D\n\nS"},
		{name: "multiple strings exact separator", payload: `{"messages":[{"role":"developer","content":"one"},{"role":"system","content":"two"},{"role":"developer","content":"three"}]}`, wantChange: true, wantHad: true, wantRemove: true, wantReason: InstructionNormalizationReasonNormalized, wantRoles: []string{"system"}, wantText: "one\n\ntwo\n\nthree"},
		{name: "later only developer unchanged", payload: `{"messages":[{"role":"user","content":"U"},{"role":"developer","content":"D"}]}`, wantHad: true, wantReason: InstructionNormalizationReasonLaterDeveloper, wantRoles: []string{"user", "developer"}},
		{name: "leading and later developer unchanged", payload: `{"messages":[{"role":"developer","content":"lead"},{"role":"user","content":"U"},{"role":"developer","content":"late"}]}`, wantHad: true, wantReason: InstructionNormalizationReasonLaterDeveloper, wantRoles: []string{"developer", "user", "developer"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(tt.payload)
			out, result, err := NormalizeLeadingOpenAIInstructions(input)
			if err != nil {
				t.Fatalf("NormalizeLeadingOpenAIInstructions() error = %v", err)
			}
			if result.Changed != tt.wantChange || result.HadDeveloper != tt.wantHad || result.RemovedAllDeveloper != tt.wantRemove || result.Reason != tt.wantReason {
				t.Fatalf("result = %+v, want Changed=%v HadDeveloper=%v RemovedAllDeveloper=%v Reason=%q", result, tt.wantChange, tt.wantHad, tt.wantRemove, tt.wantReason)
			}
			if !tt.wantChange && !bytes.Equal(out, input) {
				t.Fatalf("unchanged output differs: got %s want %s", out, input)
			}
			messages := gjson.GetBytes(out, "messages").Array()
			if len(messages) != len(tt.wantRoles) {
				t.Fatalf("roles length = %d, want %d; output=%s", len(messages), len(tt.wantRoles), out)
			}
			for index, role := range tt.wantRoles {
				if got := messages[index].Get("role").String(); got != role {
					t.Fatalf("messages.%d.role = %q, want %q; output=%s", index, got, role, out)
				}
			}
			if tt.wantText != "" {
				if got := gjson.GetBytes(out, "messages.0.content").String(); got != tt.wantText {
					t.Fatalf("merged content = %q, want %q", got, tt.wantText)
				}
			}
		})
	}
}

func TestNormalizeLeadingOpenAIInstructions_MixedTextContentAndCacheControl(t *testing.T) {
	input := []byte(`{"messages":[{"role":"system","content":"plain"},{"role":"developer","content":[{"type":"text","text":"cached","cache_control":{"type":"ephemeral"}}],"cache_control":{"type":"ephemeral","ttl":"1h"}},{"role":"user","content":"question","meta":{"keep":true}}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"reasoning":{"effort":"high"},"metadata":{"trace":"keep"}}`)

	out, result, err := NormalizeLeadingOpenAIInstructions(input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !result.RemovedAllDeveloper {
		t.Fatalf("unexpected result: %+v", result)
	}
	content := gjson.GetBytes(out, "messages.0.content")
	if !content.IsArray() || len(content.Array()) != 2 {
		t.Fatalf("merged content = %s, want two parts", content.Raw)
	}
	if got := content.Get("0.text").String(); got != "plain" {
		t.Fatalf("first text = %q, want plain", got)
	}
	if got := content.Get("1.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("part cache_control.type = %q, want ephemeral", got)
	}
	if ttl := content.Get("1.cache_control.ttl"); ttl.Exists() {
		t.Fatalf("part cache_control.ttl = %s, want absent", ttl.Raw)
	}
	if gjson.GetBytes(out, "messages.1").Raw != gjson.GetBytes(input, "messages.2").Raw {
		t.Fatal("conversational message changed")
	}
	for _, path := range []string{"tools", "reasoning", "metadata"} {
		if gjson.GetBytes(out, path).Raw != gjson.GetBytes(input, path).Raw {
			t.Fatalf("unrelated subtree %s changed", path)
		}
	}
}

func TestNormalizeLeadingOpenAIInstructions_PreservesNonTextContent(t *testing.T) {
	input := []byte(`{"messages":[{"role":"developer","content":[{"type":"text","text":"rule"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]},{"role":"user","content":"question"}]}`)
	out, result, err := NormalizeLeadingOpenAIInstructions(input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Reason != InstructionNormalizationReasonNormalized {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.image_url.url").String(); got != "data:image/png;base64,abc" {
		t.Fatalf("image URL = %q, want preserved", got)
	}
}

func TestNormalizeLeadingOpenAIInstructions_InvalidMessageContainers(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantReason InstructionNormalizationReason
	}{
		{name: "invalid JSON", input: `{"messages":[`, wantReason: InstructionNormalizationReasonInvalidJSON},
		{name: "missing messages", input: `{"model":"gpt-4.1"}`, wantReason: InstructionNormalizationReasonMissingMessages},
		{name: "messages object", input: `{"messages":{}}`, wantReason: InstructionNormalizationReasonMessagesNotArray},
		{name: "messages null", input: `{"messages":null}`, wantReason: InstructionNormalizationReasonMessagesNotArray},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(tt.input)
			out, result, err := NormalizeLeadingOpenAIInstructions(input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(out, input) {
				t.Fatalf("output = %q, want original %q", out, input)
			}
			if result.Reason != tt.wantReason || result.Changed || result.HadDeveloper || result.RemovedAllDeveloper {
				t.Fatalf("unexpected result: %+v, want reason %q", result, tt.wantReason)
			}
		})
	}
}

package common

import (
	"strings"
	"testing"
)

func TestNormalizeKiroModelID(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		" kiro/CLAUDE-4-5-SONNET-20250929-high ": "claude-sonnet-4.5",
		"kiro-claude-opus-5-max":                 "claude-opus-5",
		"amazonq-claude-sonnet-4-6":              "claude-sonnet-4.6",
		"claude-sonnet-4-6-latest":               "claude-sonnet-4.6",
		"kiro-gpt-5-6-sol-xhigh":                 "gpt-5.6-sol",
		"kiro-claude-sonnet-4-5-agentic":         "claude-sonnet-4.5",
		"kiro-claude-opus-4-6-thinking[1m]":      "claude-opus-4.6",
		"kiro-auto":                              "auto",
		"vendor-future-model":                    "vendor-future-model",
	}

	for input, want := range tests {
		input, want := input, want
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeKiroModelID(input); got != want {
				t.Fatalf("NormalizeKiroModelID(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestSanitizeKiroToolSchema(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{},
		"properties": map[string]any{
			"query": map[string]any{
				"type":                 "string",
				"additionalProperties": true,
			},
			"nested": map[string]any{
				"type":     "object",
				"required": []any{"value"},
			},
		},
	}

	got := SanitizeKiroToolSchema(input)
	if _, ok := got["additionalProperties"]; ok {
		t.Fatal("root additionalProperties was not removed")
	}
	if _, ok := got["required"]; ok {
		t.Fatal("empty required array was not removed")
	}

	properties := got["properties"].(map[string]any)
	query := properties["query"].(map[string]any)
	if _, ok := query["additionalProperties"]; ok {
		t.Fatal("nested additionalProperties was not removed")
	}
	nested := properties["nested"].(map[string]any)
	if gotRequired := nested["required"].([]any); len(gotRequired) != 1 || gotRequired[0] != "value" {
		t.Fatalf("non-empty required array changed: %#v", gotRequired)
	}

	if _, ok := input["additionalProperties"]; !ok {
		t.Fatal("sanitizer mutated its input")
	}
}

func TestNormalizeKiroToolIdentifiers(t *testing.T) {
	t.Parallel()

	longName := "mcp__browser__" + strings.Repeat("very_long_namespace_", 8) + "open_page"
	normalizedName := NormalizeKiroToolName(longName)
	if len(normalizedName) > KiroToolNameMaxLength {
		t.Fatalf("normalized tool name length = %d, max = %d", len(normalizedName), KiroToolNameMaxLength)
	}
	if normalizedName == NormalizeKiroToolName(longName+"-different") {
		t.Fatal("distinct long names collided")
	}
	if got := NormalizeKiroToolUseID("call:weird/id with spaces"); got != "call_weird_id_with_spaces" {
		t.Fatalf("NormalizeKiroToolUseID returned %q", got)
	}
	if got := NormalizeKiroToolUseID(strings.Repeat("x", 100)); len(got) != KiroToolUseIDMaxLength {
		t.Fatalf("normalized tool use id length = %d, want %d", len(got), KiroToolUseIDMaxLength)
	}
}

func TestLimitKiroImagesDropsOldestOverCaps(t *testing.T) {
	t.Parallel()

	images := make([]KiroImage, 0, KiroMaxImagesPerMessage+1)
	for i := 0; i < KiroMaxImagesPerMessage+1; i++ {
		images = append(images, KiroImage{
			Format: "png",
			Source: KiroImageSource{Bytes: strings.Repeat("a", 16)},
		})
	}

	kept, dropped := LimitKiroImages(images)
	if len(kept) != KiroMaxImagesPerMessage {
		t.Fatalf("kept %d images, want %d", len(kept), KiroMaxImagesPerMessage)
	}
	if dropped != 1 {
		t.Fatalf("dropped %d images, want 1", dropped)
	}

	oversized := []KiroImage{
		{Format: "png", Source: KiroImageSource{Bytes: strings.Repeat("a", KiroImageBase64BudgetBytes)}},
		{Format: "png", Source: KiroImageSource{Bytes: "newest"}},
	}
	kept, dropped = LimitKiroImages(oversized)
	if len(kept) != 1 || kept[0].Source.Bytes != "newest" || dropped != 1 {
		t.Fatalf("budget trimming kept=%#v dropped=%d", kept, dropped)
	}
}

func TestKiroModelContextWindow(t *testing.T) {
	t.Parallel()

	if got := KiroModelContextWindow("kiro-auto"); got != 0 {
		t.Fatalf("kiro-auto context window = %d, want 0 until the concrete model is known", got)
	}
	if got := KiroModelContextWindow("kiro-claude-sonnet-5-high"); got != 666_667 {
		t.Fatalf("sonnet 5 context window = %d, want 666667", got)
	}
}

package executor

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestParseModelEntryPreservesMaxModeAliasesWindowAndModalities(t *testing.T) {
	var data []byte
	data = protowire.AppendString(protowire.AppendTag(data, 1, protowire.BytesType), "claude-4.6-opus")
	data = protowire.AppendString(protowire.AppendTag(data, 4, protowire.BytesType), "Claude Opus")
	data = protowire.AppendString(protowire.AppendTag(data, 6, protowire.BytesType), "opus-alias")
	data = protowire.AppendVarint(protowire.AppendTag(data, 7, protowire.VarintType), 1)

	info := parseModelEntry(data)
	if info == nil {
		t.Fatal("parseModelEntry returned nil")
	}
	if info.ContextLength != 1_000_000 {
		t.Fatalf("context length = %d, want 1_000_000 for maxMode claude id", info.ContextLength)
	}
	if got := strings.Join(info.SupportedInputModalities, ","); got != "text,image" {
		t.Fatalf("modalities = %q, want text,image", got)
	}
	if !lookupCursorMaxMode("claude-4.6-opus", "") {
		t.Fatal("maxMode was not remembered for the parsed model id")
	}
	if !lookupCursorMaxMode("opus-alias", "") {
		t.Fatal("maxMode was not remembered for the parsed alias")
	}

	var labeled []byte
	labeled = protowire.AppendString(protowire.AppendTag(labeled, 1, protowire.BytesType), "gpt-5.2-1m")
	labeled = protowire.AppendString(protowire.AppendTag(labeled, 4, protowire.BytesType), "GPT 5.2")
	gpt := parseModelEntry(labeled)
	if gpt.ContextLength != 1_000_000 {
		t.Fatalf("labeled 1m context = %d, want 1_000_000", gpt.ContextLength)
	}
	if got := strings.Join(gpt.SupportedInputModalities, ","); got != "text,image" {
		t.Fatalf("gpt modalities = %q, want text,image", got)
	}
}

func TestParseModelsResponseKeepsUnfilteredFamilies(t *testing.T) {
	var entry []byte
	entry = protowire.AppendString(protowire.AppendTag(entry, 1, protowire.BytesType), "gpt-4o")
	entry = protowire.AppendString(protowire.AppendTag(entry, 4, protowire.BytesType), "GPT-4o")
	var body []byte
	body = protowire.AppendBytes(protowire.AppendTag(body, 1, protowire.BytesType), entry)

	models := parseModelsResponse(body)
	if len(models) != 1 || models[0].ID != "gpt-4o" {
		t.Fatalf("parseModelsResponse = %+v, want unfiltered gpt-4o", models)
	}
	filtered := FilterCursorModels(models)
	if len(filtered) != 0 {
		t.Fatalf("FilterCursorModels(gpt-4o) = %+v, want empty so live catalog must skip the filter", filtered)
	}
}

func TestBuildRunRequestParamsSkipsNativeCursorTools(t *testing.T) {
	parsed := parseOpenAIRequest([]byte(`{
		"model": "composer-2.5",
		"messages": [{"role":"user","content":"hi"}],
		"tools": [
			{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"search_docs","parameters":{"type":"object"}}}
		]
	}`))
	params := buildRunRequestParams(parsed, "conv-1", false)
	if len(params.McpTools) != 1 || params.McpTools[0].Name != "search_docs" {
		t.Fatalf("mcp tools = %+v, want only search_docs", params.McpTools)
	}
}

func TestParseOpenAIRequestBuildsTypedToolStepsWithoutClientPrefix(t *testing.T) {
	parsed := parseOpenAIRequest([]byte(`{
		"model": "composer-2.5",
		"messages": [
			{"role":"user","content":"find it"},
			{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"search_docs","arguments":"{\"q\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"call-1","name":"search_docs","content":"found"},
			{"role":"user","content":"thanks"}
		]
	}`))
	if len(parsed.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(parsed.Turns))
	}
	if len(parsed.Turns[0].Steps) != 1 {
		t.Fatalf("steps = %+v, want one tool step", parsed.Turns[0].Steps)
	}
	step := parsed.Turns[0].Steps[0]
	if step.ToolName != "search_docs" || step.ToolCallId != "call-1" || step.ToolResult != "found" {
		t.Fatalf("tool step = %+v", step)
	}

	roots := buildCursorRootPromptMessages(parsed)
	joined := string(bytesJoin(roots))
	if strings.Contains(joined, "ocx_client_") {
		t.Fatalf("root prompt still prefixes client tools: %s", joined)
	}
	if !strings.Contains(joined, `"toolName":"search_docs"`) {
		t.Fatalf("root prompt missing unprefixed tool name: %s", joined)
	}
}

func bytesJoin(parts [][]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

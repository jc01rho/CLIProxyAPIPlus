package claude

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestBuildKiroPayloadSanitizesAndCapsToolCatalog(t *testing.T) {
	t.Parallel()

	tools := make([]map[string]any, 0, 50)
	for i := 0; i < 50; i++ {
		tools = append(tools, map[string]any{
			"name":        fmt.Sprintf("tool_%02d", i),
			"description": strings.Repeat("description ", 100),
			"input_schema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{},
			},
		})
	}
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hello"}},
		"tools":    tools,
	})

	payload, _ := BuildKiroPayload(body, "claude-sonnet-4.6", "", "CLI", false, false, http.Header{}, nil)
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}

	current := decoded["conversationState"].(map[string]any)["currentMessage"].(map[string]any)
	user := current["userInputMessage"].(map[string]any)
	context := user["userInputMessageContext"].(map[string]any)
	gotTools := context["tools"].([]any)
	if len(gotTools) != 48 {
		t.Fatalf("tool count = %d, want 48", len(gotTools))
	}

	first := gotTools[0].(map[string]any)["toolSpecification"].(map[string]any)
	schema := first["inputSchema"].(map[string]any)["json"].(map[string]any)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatal("additionalProperties was not removed")
	}
	if _, ok := schema["required"]; ok {
		t.Fatal("empty required was not removed")
	}
	if got := len(first["description"].(string)); got != 1200 {
		t.Fatalf("description length = %d, want 1200 below Kiro CLI's 10000-character move threshold", got)
	}
}

func TestBuildKiroPayloadCarriesToolResultImagesAndAppliesImageCap(t *testing.T) {
	t.Parallel()

	content := make([]map[string]any, 0, 21)
	content = append(content, map[string]any{
		"type":        "tool_result",
		"tool_use_id": "tool_1",
		"content": []map[string]any{
			{"type": "text", "text": "done"},
			{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": "image/png",
					"data":       "tool-result-image",
				},
			},
		},
	})
	for i := 0; i < 20; i++ {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": "image/png",
				"data":       fmt.Sprintf("direct-%02d", i),
			},
		})
	}
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "tool_use", "id": "tool_1", "name": "inspect", "input": map[string]any{}},
				},
			},
			{"role": "user", "content": content},
		},
	})

	payload, _ := BuildKiroPayload(body, "claude-sonnet-4.6", "", "CLI", false, false, http.Header{}, nil)
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	current := decoded["conversationState"].(map[string]any)["currentMessage"].(map[string]any)
	user := current["userInputMessage"].(map[string]any)
	images := user["images"].([]any)
	if len(images) != 20 {
		t.Fatalf("image count = %d, want 20", len(images))
	}
	last := images[len(images)-1].(map[string]any)["source"].(map[string]any)["bytes"].(string)
	if last != "direct-19" {
		t.Fatalf("newest image = %q, want direct-19", last)
	}
}

func TestProcessToolUseEventRejectsInterleavedUnstoppedTool(t *testing.T) {
	t.Parallel()

	processed := map[string]bool{}
	_, state, err := ProcessToolUseEvent(map[string]any{
		"toolUseId": "tool_1",
		"name":      "first",
		"input":     `{"value":`,
	}, nil, processed)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = ProcessToolUseEvent(map[string]any{
		"toolUseId": "tool_2",
		"name":      "second",
		"input":     `{}`,
	}, state, processed)
	if err == nil {
		t.Fatal("expected interleaved unstopped tool error")
	}
}

func TestBuildKiroPayloadPreservesSignedThinkingInHistory(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{"role":"user","content":"question"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"private reasoning","signature":"signed-value"},
				{"type":"text","text":"answer"}
			]},
			{"role":"user","content":"continue"}
		]
	}`)

	payload, _ := BuildKiroPayload(body, "claude-sonnet-4.6", "", "AI_EDITOR", false, false, http.Header{}, nil)
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	history := decoded["conversationState"].(map[string]any)["history"].([]any)
	assistant := history[1].(map[string]any)["assistantResponseMessage"].(map[string]any)
	reasoning := assistant["reasoningContent"].(map[string]any)["reasoningText"].(map[string]any)
	if got := reasoning["text"]; got != "private reasoning" {
		t.Fatalf("reasoning text = %#v", got)
	}
	if got := reasoning["signature"]; got != "signed-value" {
		t.Fatalf("reasoning signature = %#v", got)
	}
}

func TestBuildKiroPayloadMovesVeryLongToolDocsToSystemPrompt(t *testing.T) {
	t.Parallel()

	longDescription := strings.Repeat("documentation-", 900)
	body, err := json.Marshal(map[string]any{
		"system":   "base system",
		"messages": []map[string]any{{"role": "user", "content": "hello"}},
		"tools": []map[string]any{{
			"name":         "long_tool",
			"description":  longDescription,
			"input_schema": map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := BuildKiroPayload(body, "claude-sonnet-4.6", "", "AI_EDITOR", false, false, http.Header{}, nil)
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	current := decoded["conversationState"].(map[string]any)["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	if !strings.Contains(current["content"].(string), "## Tool: long_tool") {
		t.Fatal("full tool documentation was not moved into the system prompt")
	}
	tools := current["userInputMessageContext"].(map[string]any)["tools"].([]any)
	description := tools[0].(map[string]any)["toolSpecification"].(map[string]any)["description"].(string)
	if !strings.Contains(description, "moved to the system prompt") {
		t.Fatalf("tool description pointer = %q", description)
	}
}

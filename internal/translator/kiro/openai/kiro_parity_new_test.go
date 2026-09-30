package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestOpenAILaterSystemMessageStaysInConversation(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"base"},{"role":"user","content":"first"},{"role":"developer","content":"later"},{"role":"user","content":"current"}]}`)
	result, _ := BuildKiroPayloadFromOpenAI(body, "model", "", "AI_EDITOR", false, false, nil, nil)
	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	contents := make([]string, 0, len(payload.ConversationState.History)+1)
	for _, entry := range payload.ConversationState.History {
		if entry.UserInputMessage != nil {
			contents = append(contents, entry.UserInputMessage.Content)
		}
	}
	contents = append(contents, payload.ConversationState.CurrentMessage.UserInputMessage.Content)
	joined := strings.Join(contents, "\n")
	if !strings.Contains(joined, "base") || !strings.Contains(joined, "<system-reminder>") || !strings.Contains(joined, "later") {
		t.Fatalf("conversation content = %q", joined)
	}
	if strings.Contains(string(result), "Current time is") {
		t.Fatalf("unstable timestamp remained: %s", result)
	}
}

func TestOpenAIGPTNativeReasoningEffort(t *testing.T) {
	for _, effort := range []string{"high", "none", "off", "disabled", "0"} {
		body := []byte(`{"reasoning_effort":"` + effort + `","messages":[{"role":"user","content":"hi"}]}`)
		result, _ := BuildKiroPayloadFromOpenAI(body, "gpt-5.6-sol", "", "AI_EDITOR", false, false, nil, nil)
		var payload KiroPayload
		if err := json.Unmarshal(result, &payload); err != nil {
			t.Fatal(err)
		}
		got := payload.AdditionalModelRequestFields["reasoning"].(map[string]any)["effort"]
		want := effort
		if effort != "high" {
			want = "none"
		}
		if got != want {
			t.Fatalf("effort %q = %#v, want %q", effort, got, want)
		}
	}
}

func TestOpenAIForwardsClaudeThinkingSignature(t *testing.T) {
	raw := []byte(`{"content":[{"type":"thinking","thinking":"secret","signature":"real-signature"}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	body := ConvertKiroNonStreamToOpenAI(nil, "model", nil, nil, raw, nil)
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	message := response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if message["reasoning_signature"] != "real-signature" {
		t.Fatalf("reasoning signature = %#v", message["reasoning_signature"])
	}
}

func TestOpenAIUsageCarriesCacheCounters(t *testing.T) {
	body := BuildOpenAIResponse("", nil, "model", usage.Detail{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheCreationTokens: 4}, "end_turn")
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	usageMap := response["usage"].(map[string]any)
	if usageMap["cache_read_input_tokens"] != float64(3) || usageMap["cache_creation_input_tokens"] != float64(4) {
		t.Fatalf("usage = %#v", usageMap)
	}
}

package claude

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestClaudeLaterSystemMessageStaysInConversation(t *testing.T) {
	body := []byte(`{"system":"base","messages":[{"role":"user","content":"first"},{"role":"system","content":"later"},{"role":"developer","content":[{"type":"text","text":"later-2"}]},{"role":"user","content":"current"}]}`)
	result, _ := BuildKiroPayload(body, "model", "", "AI_EDITOR", false, false, nil, nil)
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
	if !strings.Contains(joined, "base") || !strings.Contains(joined, "<system-reminder>\nlater\n</system-reminder>") || !strings.Contains(joined, "<system-reminder>\nlater-2\n</system-reminder>") {
		t.Fatalf("conversation content = %q", joined)
	}
	if strings.Contains(string(result), "Current time is") {
		t.Fatalf("unstable timestamp remained: %s", result)
	}
}

func TestBuildClaudeSignatureDeltaEvent(t *testing.T) {
	got := string(BuildClaudeSignatureDeltaEvent("real-signature", 2))
	if !strings.HasPrefix(got, "event: content_block_delta\ndata: ") || !strings.Contains(got, `"index":2`) || !strings.Contains(got, `"type":"signature_delta"`) || !strings.Contains(got, `"signature":"real-signature"`) {
		t.Fatalf("signature SSE = %s", got)
	}
}

func TestBuildClaudeResponseUsesProvidedSignaturesAndCacheUsage(t *testing.T) {
	body := BuildClaudeResponseWithThinkingSignatures("before<thinking>one</thinking>middle<thinking>two</thinking>after", []string{"real-1", "real-2"}, nil, "model", usage.Detail{InputTokens: 3, OutputTokens: 4, CacheReadTokens: 5, CacheCreationTokens: 6}, "end_turn")
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	blocks := response["content"].([]any)
	var signatures []string
	for _, block := range blocks {
		contentBlock := block.(map[string]any)
		if contentBlock["type"] == "thinking" {
			signatures = append(signatures, contentBlock["signature"].(string))
		}
	}
	if !reflect.DeepEqual(signatures, []string{"real-1", "real-2"}) {
		t.Fatalf("signatures = %#v", signatures)
	}
	usageMap := response["usage"].(map[string]any)
	if !reflect.DeepEqual(usageMap, map[string]any{"input_tokens": float64(3), "output_tokens": float64(4), "cache_read_input_tokens": float64(5), "cache_creation_input_tokens": float64(6)}) {
		t.Fatalf("usage = %#v", usageMap)
	}
}

func TestBuildClaudeResponseOnlyHashesMissingSignature(t *testing.T) {
	body := BuildClaudeResponseWithThinkingSignatures("<thinking>signed</thinking><thinking>unsigned</thinking>", []string{"real-signature"}, nil, "model", usage.Detail{}, "end_turn")
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	blocks := response["content"].([]any)
	first := blocks[0].(map[string]any)["signature"].(string)
	second := blocks[1].(map[string]any)["signature"].(string)
	hash := sha256.Sum256([]byte("unsigned"))
	if first != "real-signature" || second != base64.StdEncoding.EncodeToString(hash[:]) {
		t.Fatalf("signatures = %q, %q", first, second)
	}
}

func TestBuildClaudeUsageIncludesZeroCacheCounters(t *testing.T) {
	if got := BuildClaudeUsage(usage.Detail{InputTokens: 1, OutputTokens: 2}); !reflect.DeepEqual(got, map[string]interface{}{"input_tokens": int64(1), "output_tokens": int64(2), "cache_read_input_tokens": int64(0), "cache_creation_input_tokens": int64(0)}) {
		t.Fatalf("usage = %#v", got)
	}
}

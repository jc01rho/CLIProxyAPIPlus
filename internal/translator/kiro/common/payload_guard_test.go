package common

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestGuardKiroPayloadRejectsOversizedPayload(t *testing.T) {
	t.Setenv("KIRO_MAX_PAYLOAD_TOKENS", "0")
	t.Setenv("KIRO_MAX_PAYLOAD_BYTES", "256")
	t.Setenv("AUTO_TRIM_PAYLOAD", "false")

	payload := mustMarshalKiroPayload(t, map[string]any{
		"conversationState": map[string]any{
			"chatTriggerType": "MANUAL",
			"conversationId":  "conversation",
			"currentMessage": map[string]any{
				"userInputMessage": map[string]any{
					"content": strings.Repeat("x", 512),
					"modelId": "claude-sonnet-4.6",
					"origin":  "AI_EDITOR",
				},
			},
		},
	})

	_, _, err := GuardKiroPayload(payload, "claude-sonnet-4.6")
	var tooLarge *KiroPayloadTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("GuardKiroPayload error = %v, want KiroPayloadTooLargeError", err)
	}
	if tooLarge.Bytes <= tooLarge.ByteLimit {
		t.Fatalf("payload bytes = %d, byte limit = %d", tooLarge.Bytes, tooLarge.ByteLimit)
	}
}

func TestGuardKiroPayloadAutoTrimDropsOldestPairsAndRepairsTools(t *testing.T) {
	t.Setenv("KIRO_MAX_PAYLOAD_TOKENS", "0")
	t.Setenv("KIRO_MAX_PAYLOAD_BYTES", "720")
	t.Setenv("AUTO_TRIM_PAYLOAD", "true")

	payload := mustMarshalKiroPayload(t, map[string]any{
		"conversationState": map[string]any{
			"chatTriggerType": "MANUAL",
			"conversationId":  "conversation",
			"history": []any{
				map[string]any{"userInputMessage": map[string]any{"content": strings.Repeat("old-user-", 30), "modelId": "claude-sonnet-4.6", "origin": "AI_EDITOR"}},
				map[string]any{"assistantResponseMessage": map[string]any{"content": strings.Repeat("old-assistant-", 30), "toolUses": []any{}}},
				map[string]any{"userInputMessage": map[string]any{"content": "kept user", "modelId": "claude-sonnet-4.6", "origin": "AI_EDITOR"}},
				map[string]any{"assistantResponseMessage": map[string]any{"content": "kept assistant", "toolUses": []any{map[string]any{"toolUseId": "call-kept", "name": "bash", "input": map[string]any{}}}}},
				map[string]any{"userInputMessage": map[string]any{
					"content": "result",
					"modelId": "claude-sonnet-4.6",
					"origin":  "AI_EDITOR",
					"userInputMessageContext": map[string]any{"toolResults": []any{
						map[string]any{"toolUseId": "call-kept", "status": "success", "content": []any{map[string]any{"text": "kept"}}},
						map[string]any{"toolUseId": "call-old", "status": "success", "content": []any{map[string]any{"text": "orphan"}}},
					}},
				}},
			},
			"currentMessage": map[string]any{
				"userInputMessage": map[string]any{"content": "continue", "modelId": "claude-sonnet-4.6", "origin": "AI_EDITOR"},
			},
		},
	})

	guarded, stats, err := GuardKiroPayload(payload, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Trimmed || stats.FinalHistoryEntries >= stats.OriginalHistoryEntries {
		t.Fatalf("unexpected trim stats: %#v", stats)
	}

	var decoded map[string]any
	if err := json.Unmarshal(guarded, &decoded); err != nil {
		t.Fatal(err)
	}
	history := decoded["conversationState"].(map[string]any)["history"].([]any)
	if _, ok := history[0].(map[string]any)["userInputMessage"]; !ok {
		t.Fatalf("trimmed history does not start with userInputMessage: %#v", history[0])
	}
	encoded := string(guarded)
	if strings.Contains(encoded, `"toolUseId":"call-old"`) {
		t.Fatalf("orphaned tool result remained structured: %s", encoded)
	}
	if !strings.Contains(encoded, "[trimmed tool result] orphan") {
		t.Fatalf("orphaned tool result text was not preserved: %s", encoded)
	}
}

func mustMarshalKiroPayload(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

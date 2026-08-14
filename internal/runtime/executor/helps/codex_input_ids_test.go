package helps

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

var benchmarkSanitizeCodexInputItemIDsOutput []byte

func TestSanitizeCodexInputItemIDsBoundaries(t *testing.T) {
	id64 := strings.Repeat("a", 64)
	id65 := strings.Repeat("b", 65)
	unicode65 := strings.Repeat("界", 65)
	body := []byte(`{"input":[{"id":"` + id64 + `"},{"id":"` + id65 + `"},{"id":"` + unicode65 + `"}]}`)

	got := SanitizeCodexInputItemIDs(body)

	if actual := gjson.GetBytes(got, "input.0.id").String(); actual != id64 {
		t.Fatalf("64-character ID changed: %q", actual)
	}
	for _, path := range []string{"input.1.id", "input.2.id"} {
		actual := gjson.GetBytes(got, path).String()
		if len([]rune(actual)) != 64 {
			t.Fatalf("%s length = %d, want 64: %q", path, len([]rune(actual)), actual)
		}
	}
}

func TestSanitizeCodexInputItemIDsNormalizesMessageIDs(t *testing.T) {
	const invalidID = "item_74ec40c883248ebb4885ec84"
	body := []byte(`{"input":[` +
		`{"type":"message","id":"` + invalidID + `","role":"user"},` +
		`{"type":"message","id":"msg-1","role":"assistant"},` +
		`{"type":"function_call","id":"item_call","call_id":"call-1"}` +
		`]}`)

	first := SanitizeCodexInputItemIDs(body)
	second := SanitizeCodexInputItemIDs(body)

	if got := gjson.GetBytes(first, "input.0.id").String(); got != "msg_"+invalidID {
		t.Fatalf("message ID = %q, want msg-prefixed ID", got)
	}
	if got := gjson.GetBytes(first, "input.1.id").String(); got != "msg-1" {
		t.Fatalf("valid message ID changed: %q", got)
	}
	if got := gjson.GetBytes(first, "input.2.id").String(); got != "fc_item_call" {
		t.Fatalf("function_call ID was not normalized: %q", got)
	}
	if string(first) != string(second) {
		t.Fatalf("message ID normalization is not deterministic: first=%s second=%s", first, second)
	}
}

func TestSanitizeCodexInputItemIDsNormalizesInvalidFunctionCallIDs(t *testing.T) {
	rawIDs := []string{
		"fc_functions.read:60",
		"fc_functions/read:60",
	}
	body := []byte(`{"input":[` +
		`{"type":"function_call","id":"` + rawIDs[0] + `","call_id":"call-read-60"},` +
		`{"type":"function_call","id":"` + rawIDs[1] + `","call_id":"call-read-61"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)
	seen := make(map[string]struct{}, len(rawIDs))
	for index, rawID := range rawIDs {
		id := gjson.GetBytes(got, fmt.Sprintf("input.%d.id", index)).String()
		if id == rawID {
			t.Fatalf("function call ID %q was not normalized", rawID)
		}
		if !strings.HasPrefix(id, "fc_") {
			t.Errorf("function call ID %q does not retain fc_ prefix", id)
		}
		if len([]rune(id)) > codexInputItemIDLimit {
			t.Errorf("function call ID %q exceeds %d characters", id, codexInputItemIDLimit)
		}
		for _, char := range id {
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
				t.Errorf("function call ID %q contains invalid character %q", id, char)
			}
		}
		if _, exists := seen[id]; exists {
			t.Errorf("distinct function call IDs normalized to duplicate %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestSanitizeCodexInputItemIDsNormalizesResponseItemIDs(t *testing.T) {
	const (
		messageID            = "item_message"
		reasoningID          = "item_reasoning"
		functionCallID       = "item_function_call"
		functionCallOutputID = "item_function_call_output"
	)
	body := []byte(`{"input":[` +
		`{"type":"message","id":"` + messageID + `"},` +
		`{"type":"reasoning","id":"` + reasoningID + `"},` +
		`{"type":"function_call","id":"` + functionCallID + `","call_id":"call-1"},` +
		`{"type":"function_call_output","id":"` + functionCallOutputID + `","call_id":"call-1"},` +
		`{"type":"reasoning","id":"rs-existing"},` +
		`{"type":"function_call","id":"fc-existing","call_id":"call-2"},` +
		`{"type":"message","id":"msg-existing"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)
	want := []string{
		"msg_" + messageID,
		"rs_" + reasoningID,
		"fc_" + functionCallID,
		functionCallOutputID,
		"rs-existing",
		"fc-existing",
		"msg-existing",
	}

	for index, expected := range want {
		path := fmt.Sprintf("input.%d.id", index)
		if actual := gjson.GetBytes(got, path).String(); actual != expected {
			t.Fatalf("%s = %q, want %q; payload=%s", path, actual, expected, got)
		}
	}

	if second := SanitizeCodexInputItemIDs(body); string(second) != string(got) {
		t.Fatalf("normalization is not deterministic: first=%s second=%s", got, second)
	}
}

func TestSanitizeCodexInputItemIDsAvoidsNormalizationCollisions(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		itemType string
		prefix   string
	}{
		{name: "message", itemType: "message", prefix: "msg_"},
		{name: "reasoning", itemType: "reasoning", prefix: "rs_"},
		{name: "function call", itemType: "function_call", prefix: "fc_"},
		{name: "custom tool call", itemType: "custom_tool_call", prefix: "ctc_"},
		{name: "custom tool call output", itemType: "custom_tool_call_output", prefix: "ctco_"},
	} {
		for _, idCase := range []struct {
			name      string
			invalidID string
		}{
			{name: "short", invalidID: "item_collision"},
			{name: "overlong", invalidID: strings.Repeat("x", codexInputItemIDLimit-len([]rune(testCase.prefix))+1)},
		} {
			prefixedID := testCase.prefix + idCase.invalidID
			for _, order := range []struct {
				name          string
				ids           [2]string
				prefixedIndex int
			}{
				{name: "local first", ids: [2]string{idCase.invalidID, prefixedID}, prefixedIndex: 1},
				{name: "prefixed first", ids: [2]string{prefixedID, idCase.invalidID}, prefixedIndex: 0},
			} {
				t.Run(testCase.name+"/"+idCase.name+"/"+order.name, func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"input":[{"type":%q,"id":%q},{"type":%q,"id":%q}]}`, testCase.itemType, order.ids[0], testCase.itemType, order.ids[1]))

					first := SanitizeCodexInputItemIDs(body)
					second := SanitizeCodexInputItemIDs(body)
					normalizedAgain := SanitizeCodexInputItemIDs(first)
					ids := [2]string{
						gjson.GetBytes(first, "input.0.id").String(),
						gjson.GetBytes(first, "input.1.id").String(),
					}

					if ids[0] == ids[1] {
						t.Fatalf("distinct IDs collided after normalization: %q; payload=%s", ids[0], first)
					}
					for index, id := range ids {
						if !strings.HasPrefix(id, testCase.prefix) {
							t.Fatalf("input.%d.id = %q, want prefix %q", index, id, testCase.prefix)
						}
						if len([]rune(id)) > codexInputItemIDLimit {
							t.Fatalf("input.%d.id length = %d, want at most %d: %q", index, len([]rune(id)), codexInputItemIDLimit, id)
						}
					}
					if len([]rune(prefixedID)) <= codexInputItemIDLimit && ids[order.prefixedIndex] != prefixedID {
						t.Fatalf("existing valid ID changed: got %q want %q", ids[order.prefixedIndex], prefixedID)
					}
					if string(first) != string(second) {
						t.Fatalf("collision resolution is not deterministic: first=%s second=%s", first, second)
					}
					if string(first) != string(normalizedAgain) {
						t.Fatalf("collision resolution is not idempotent: first=%s normalized_again=%s", first, normalizedAgain)
					}
				})
			}
		}
	}
}

func TestSanitizeCodexInputItemIDsNormalizesCustomToolCallIDs(t *testing.T) {
	const invalidID = "item_44e13caebc1ddf25f1337cbe"
	body := []byte(`{"input":[{"type":"custom_tool_call","id":"` + invalidID + `","call_id":"call-1","name":"lookup","input":"{}"}]}`)

	got := SanitizeCodexInputItemIDs(body)
	if actual := gjson.GetBytes(got, "input.0.id").String(); actual != "ctc_"+invalidID {
		t.Fatalf("custom_tool_call ID = %q, want ctc-prefixed ID", actual)
	}
}

func TestSanitizeCodexInputItemIDsNormalizesCustomToolCallOutputIDs(t *testing.T) {
	const (
		invalidID = "item_44e13caebc1ddf25f1337cbe_output"
		validID   = "ctco-existing"
	)
	body := []byte(`{"input":[` +
		`{"type":"custom_tool_call_output","id":"` + invalidID + `","call_id":"call-1","output":"done"},` +
		`{"type":"custom_tool_call_output","id":"` + validID + `","call_id":"call-2","output":"done"}` +
		`]}`)

	first := SanitizeCodexInputItemIDs(body)
	second := SanitizeCodexInputItemIDs(body)
	normalizedAgain := SanitizeCodexInputItemIDs(first)

	if actual := gjson.GetBytes(first, "input.0.id").String(); actual != "ctco_"+invalidID {
		t.Fatalf("custom_tool_call_output ID = %q, want ctco-prefixed ID", actual)
	}
	if actual := gjson.GetBytes(first, "input.1.id").String(); actual != validID {
		t.Fatalf("valid custom_tool_call_output ID changed: %q", actual)
	}
	if string(first) != string(second) {
		t.Fatalf("custom_tool_call_output ID normalization is not deterministic: first=%s second=%s", first, second)
	}
	if string(first) != string(normalizedAgain) {
		t.Fatalf("custom_tool_call_output ID normalization is not idempotent: first=%s normalized_again=%s", first, normalizedAgain)
	}
}

func TestSanitizeCodexInputItemIDsDropsOverlongEncryptedReasoningItem(t *testing.T) {
	longReasoningID := "rs_" + strings.Repeat("a", 64)
	shortReasoningID := "rs_" + strings.Repeat("b", 48)
	longCallID := strings.Repeat("call-item-", 8)
	body := []byte(`{"input":[` +
		`{"type":"message","id":"msg-1","role":"user","content":"before"},` +
		`{"type":"reasoning","id":"` + longReasoningID + `","encrypted_content":"gAAAA-encrypted","summary":[{"type":"summary_text","text":"drop me"}]},` +
		`{"type":"reasoning","id":"` + shortReasoningID + `","encrypted_content":"gAAAA-encrypted","summary":[]},` +
		`{"type":"function_call","id":"` + longCallID + `","call_id":"call-1","name":"lookup","arguments":"{}"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)
	input := gjson.GetBytes(got, "input").Array()

	if len(input) != 3 {
		t.Fatalf("input length = %d, want 3: %s", len(input), got)
	}
	if gotID := input[0].Get("id").String(); gotID != "msg-1" {
		t.Fatalf("input.0.id = %q, want msg-1", gotID)
	}
	if gotID := input[1].Get("id").String(); gotID != shortReasoningID {
		t.Fatalf("short encrypted reasoning id changed: %q", gotID)
	}
	if gotID := input[2].Get("id").String(); gotID == longCallID || len([]rune(gotID)) != 64 {
		t.Fatalf("ordinary overlong id was not shortened: %q", gotID)
	}
}

func TestSanitizeCodexInputItemIDsShortensOverlongReasoningWithoutEncryptedContent(t *testing.T) {
	longReasoningID := "rs_" + strings.Repeat("a", 64)
	for _, testCase := range []struct {
		name             string
		encryptedContent string
	}{
		{name: "missing"},
		{name: "empty", encryptedContent: `,"encrypted_content":""`},
		{name: "null", encryptedContent: `,"encrypted_content":null`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := []byte(`{"input":[{"type":"reasoning","id":"` + longReasoningID + `"` + testCase.encryptedContent + `,"summary":[]}]}`)

			got := SanitizeCodexInputItemIDs(body)
			input := gjson.GetBytes(got, "input").Array()
			if len(input) != 1 {
				t.Fatalf("input length = %d, want 1: %s", len(input), got)
			}
			gotID := input[0].Get("id").String()
			if gotID == longReasoningID || len([]rune(gotID)) != 64 {
				t.Fatalf("overlong reasoning id was not shortened: %q", gotID)
			}
		})
	}
}

func TestSanitizeCodexInputItemIDsAvoidsExistingIDCollision(t *testing.T) {
	longID := strings.Repeat("grok-item-", 10)
	collidingValidID := shortenCodexInputItemID(longID)
	body := []byte(`{"input":[{"id":"` + longID + `"},{"id":"` + collidingValidID + `"}]}`)

	first := SanitizeCodexInputItemIDs(body)
	second := SanitizeCodexInputItemIDs(body)

	shortened := gjson.GetBytes(first, "input.0.id").String()
	if shortened == collidingValidID {
		t.Fatalf("shortened ID collided with an existing valid ID: %q", shortened)
	}
	if len([]rune(shortened)) > 64 {
		t.Fatalf("shortened ID length = %d, want at most 64", len([]rune(shortened)))
	}
	if actual := gjson.GetBytes(first, "input.1.id").String(); actual != collidingValidID {
		t.Fatalf("existing valid ID changed: %q", actual)
	}
	if actual := gjson.GetBytes(second, "input.0.id").String(); actual != shortened {
		t.Fatalf("collision resolution is not deterministic: first=%q second=%q", shortened, actual)
	}
}

func TestSanitizeCodexInputItemIDsRemovesOutputOnlyFields(t *testing.T) {
	body := []byte(`{"input":[` +
		`{"type":"message","id":"msg_1","role":"developer","content":"sys"},` +
		`{"type":"message","id":"msg_2","role":"user","content":"hi"},` +
		`{"type":"message","id":"msg_3","role":"assistant","content":[{"type":"output_text","text":"ok"}],"status":"completed","phase":"final","namespace":"chat"},` +
		`{"type":"function_call","id":"fc_1","call_id":"call-1","name":"lookup","status":"in_progress","phase":"commentary"},` +
		`{"type":"function_call_output","call_id":"call-1","output":"{}","status":"completed"}` +
		`]}`)

	got := SanitizeCodexInputItemIDs(body)

	for _, path := range []string{
		"input.2.status",
		"input.2.phase",
		"input.2.namespace",
		"input.3.status",
		"input.3.phase",
		"input.4.status",
	} {
		if gjson.GetBytes(got, path).Exists() {
			t.Fatalf("%s should be stripped: %s", path, got)
		}
	}
	if gotID := gjson.GetBytes(got, "input.2.id").String(); gotID != "msg_3" {
		t.Fatalf("input.2.id = %q, want msg_3", gotID)
	}
	if gotRole := gjson.GetBytes(got, "input.2.role").String(); gotRole != "assistant" {
		t.Fatalf("input.2.role = %q, want assistant", gotRole)
	}
	if gotID := gjson.GetBytes(got, "input.3.id").String(); gotID != "fc_1" {
		t.Fatalf("input.3.id = %q, want fc_1", gotID)
	}
	if gotCallID := gjson.GetBytes(got, "input.4.call_id").String(); gotCallID != "call-1" {
		t.Fatalf("input.4.call_id = %q, want call-1", gotCallID)
	}
}

func TestSanitizeCodexInputItemIDsLeavesUnsupportedPayloadsUnchanged(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`not-json`),
		[]byte(`{"input":{"id":"item-1"}}`),
		[]byte(`{"input":[1,{"id":2},{"id":"item-1"}]}`),
	} {
		if got := string(SanitizeCodexInputItemIDs(body)); got != string(body) {
			t.Fatalf("payload changed: got=%q want=%q", got, body)
		}
	}
}

func BenchmarkSanitizeCodexInputItemIDsLargeNoopPayload(b *testing.B) {
	body := []byte(`{"input":[{"type":"message","id":"msg_1","role":"user","content":"` + strings.Repeat("x", 8<<20) + `"}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkSanitizeCodexInputItemIDsOutput = SanitizeCodexInputItemIDs(body)
	}
}

func BenchmarkSanitizeCodexInputItemIDsLargeHistory(b *testing.B) {
	var payload strings.Builder
	payload.Grow(64 << 10)
	payload.WriteString(`{"input":[`)
	for index := range 1000 {
		if index > 0 {
			payload.WriteByte(',')
		}
		fmt.Fprintf(&payload, `{"type":"message","id":"msg_%d","role":"user","content":"x"}`, index)
	}
	payload.WriteString(`]}`)
	body := []byte(payload.String())

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkSanitizeCodexInputItemIDsOutput = SanitizeCodexInputItemIDs(body)
	}
}

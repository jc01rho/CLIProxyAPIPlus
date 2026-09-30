package common

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSplitKiroSystemMessagesOnlyHoistsLeading(t *testing.T) {
	leading, ordered := SplitKiroSystemMessages(gjson.Parse(`[
		{"role":"system","content":"prefix-1"},
		{"role":"developer","content":[{"type":"text","text":"prefix-2"}]},
		{"role":"assistant","content":"answer"},
		{"role":"system","content":"late-1"},
		{"role":"developer","content":[{"type":"text","text":"late-2"}]},
		{"role":"user","content":"question"}
	]`).Array())
	if !reflect.DeepEqual(leading, []string{"prefix-1", "prefix-2"}) || len(ordered) != 4 {
		t.Fatalf("leading=%v ordered=%v", leading, ordered)
	}
	for i, text := range []string{"late-1", "late-2"} {
		message := ordered[i+1]
		if message.Get("role").String() != "user" || message.Get("content").String() != "<system-reminder>\n"+text+"\n</system-reminder>" {
			t.Fatalf("reminder at %d = %s", i+1, message.Raw)
		}
	}
	if ordered[0].Get("content").String() != "answer" || ordered[3].Get("content").String() != "question" {
		t.Fatalf("conversation order changed: %v", ordered)
	}
	for _, role := range []string{"user", "assistant", "tool", "unknown"} {
		prefix, rest := SplitKiroSystemMessages(gjson.Parse(`[{"role":"` + role + `","content":""},{"role":"system","content":"late"}]`).Array())
		if len(prefix) != 0 || len(rest) != 2 || rest[1].Get("role").String() != "user" {
			t.Fatalf("empty %s turn must end the prefix: %v %v", role, prefix, rest)
		}
	}
}

func TestSanitizeKiroToolSchemaPreservesAdditionalProperties(t *testing.T) {
	input := map[string]any{
		"additionalProperties": map[string]any{"type": "string"},
		"anyOf":                []any{map[string]any{"additionalProperties": false}, map[string]any{"additionalProperties": true}},
	}
	got := SanitizeKiroToolSchema(input)
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("supported schema changed: %#v", got)
	}
}

func TestPlanKiroNativeReasoningExactAllowlist(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "kiro-gpt-5-6-luna-thinking"} {
		for effort, want := range map[string]string{"none": "none", " OFF ": "none", "disabled": "none", "0": "none", "minimal": "low", "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max", "invalid": ""} {
			plan := PlanKiroThinking(model, true, effort, 8000)
			if want == "" {
				if plan.Fields != nil {
					t.Fatalf("unsupported effort: %+v", plan)
				}
				continue
			}
			if !reflect.DeepEqual(plan.Fields, map[string]any{"reasoning": map[string]any{"effort": want}}) || plan.InjectPrompt {
				t.Fatalf("%s/%s: %+v", model, effort, plan)
			}
		}
	}
	for _, model := range []string{"gpt-5.6", "gpt-5.6-sol-extra", "gpt-5.7-sol", "gpt-5.6-mars", "glm-5"} {
		if plan := PlanKiroThinking(model, true, "high", 8000); plan.NativeReasoning || plan.Fields != nil {
			t.Fatalf("non-allowlisted model %s received native fields: %+v", model, plan)
		}
	}
}

func TestEstimateKiroImageTokensIsBounded(t *testing.T) {
	if got := EstimateKiroImageTokens("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLKTAAAAABJRU5ErkJggg=="); got != 1 {
		t.Fatalf("1x1 PNG estimate = %d, want 1", got)
	}
	for _, input := range []string{"", "not-base64", base64.StdEncoding.EncodeToString([]byte("unknown"))} {
		if got := EstimateKiroImageTokens(input); got != 1600 {
			t.Fatalf("unknown image estimate = %d, want 1600", got)
		}
	}
	for _, format := range []string{"VP8 ", "VP8L", "VP8X"} {
		header := make([]byte, 30)
		copy(header, "RIFF")
		copy(header[8:], "WEBP"+format)
		switch format {
		case "VP8 ":
			binary.LittleEndian.PutUint16(header[26:], 30)
			binary.LittleEndian.PutUint16(header[28:], 25)
		case "VP8L":
			binary.LittleEndian.PutUint32(header[21:], 29|24<<14)
		case "VP8X":
			header[24], header[27] = 29, 24
		}
		if got := EstimateKiroImageTokens(base64.StdEncoding.EncodeToString(header)); got != 1 {
			t.Fatalf("%s estimate = %d, want 1", format, got)
		}
	}
	huge := make([]byte, 30)
	copy(huge, "RIFF")
	copy(huge[8:], "WEBPVP8X")
	for i := 24; i < 30; i++ {
		huge[i] = 255
	}
	if got := EstimateKiroImageTokens(base64.StdEncoding.EncodeToString(huge)); got != 1600 {
		t.Fatalf("oversized WebP estimate = %d", got)
	}
}

func TestGuardPayloadMeasuresOnlyTextAndVisionAndPreservesWireData(t *testing.T) {
	// Explicit expected measurement also ensures arbitrary tool arguments named
	// signature, and signature fields outside history reasoning, still count.
	wire := []byte(`{"conversationState":{"history":[{"assistantResponseMessage":{"content":"answer","reasoningContent":{"reasoningText":{"text":"thought","signature":"OPAQUE"}},"toolUses":[{"input":{"signature":"tool-signature"}}]}}],"currentMessage":{"userInputMessage":{"content":"next","images":[{"source":{"bytes":"IMAGE"}}],"signature":"current-signature"}}},"signature":"root-signature"}`)
	wire = bytes.Replace(wire, []byte("OPAQUE"), []byte(strings.Repeat("opaque", 1000)), 1)
	wire = bytes.Replace(wire, []byte("IMAGE"), []byte(strings.Repeat("YQ==", 1000)), 1)
	var decoded map[string]any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	expected := []byte(`{"conversationState":{"history":[{"assistantResponseMessage":{"content":"answer","reasoningContent":{"reasoningText":{"text":"thought","signature":""}},"toolUses":[{"input":{"signature":"tool-signature"}}]}}],"currentMessage":{"userInputMessage":{"content":"next","images":[{"source":{"bytes":""}}],"signature":"current-signature"}}},"signature":"root-signature"}`)
	var measured map[string]any
	if err := json.Unmarshal(expected, &measured); err != nil {
		t.Fatal(err)
	}
	compact, err := marshalCompactKiroPayload(measured)
	if err != nil {
		t.Fatal(err)
	}
	textTokens, err := countKiroPayloadTokens(compact)
	if err != nil {
		t.Fatal(err)
	}
	wantTokens := textTokens + 1600
	t.Setenv("KIRO_MAX_PAYLOAD_BYTES", strconv.Itoa(len(compact)))
	t.Setenv("KIRO_MAX_PAYLOAD_TOKENS", strconv.Itoa(wantTokens))
	t.Setenv("AUTO_TRIM_PAYLOAD", "false")
	guarded, stats, err := GuardKiroPayload(wire, "model")
	if err != nil {
		t.Fatal(err)
	}
	if stats.FinalBytes != len(compact) || stats.OriginalBytes != len(compact) || stats.FinalTokens != wantTokens || stats.Trimmed {
		t.Fatalf("measurement stats = %+v, want %d bytes and %d tokens", stats, len(compact), wantTokens)
	}
	var sent map[string]any
	if err := json.Unmarshal(guarded, &sent); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, sent) {
		t.Fatal("guard changed the outgoing image/signature data")
	}
	t.Setenv("KIRO_MAX_PAYLOAD_TOKENS", strconv.Itoa(wantTokens-1))
	_, _, err = GuardKiroPayload(wire, "model")
	var tooLarge *KiroPayloadTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Tokens != wantTokens {
		t.Fatalf("vision tokens were not enforced: %v", err)
	}
}

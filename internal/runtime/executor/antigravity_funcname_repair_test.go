package executor

import (
	"context"
	"strings"
	"testing"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// TestAntigravityDropEmptyNameFunctionCallPartsDropsPairedEmptyCall verifies
// that an empty-name functionCall part and its positionally paired
// empty-name functionResponse part are both dropped, and that
// ValidateGeminiFunctionCallPairing accepts the repaired payload even though
// it rejects the original.
func TestAntigravityDropEmptyNameFunctionCallPartsDropsPairedEmptyCall(t *testing.T) {
	payload := []byte(`{"request":{"contents":[
		{"role":"user","parts":[{"text":"hi"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"","args":{}}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"","response":{"result":"ok"}}}]}
	]}}`)

	if err := internalsignature.ValidateGeminiFunctionCallPairing(payload); err == nil {
		t.Fatal("fixture should be rejected by the validator before repair")
	}

	out := antigravityDropEmptyNameFunctionCallParts(payload)

	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("validator still rejects repaired payload: %v; output=%s", err, out)
	}
	// The model content held only the empty-name call, so it is dropped
	// entirely; likewise the user content held only the paired empty-name
	// response.
	contents := gjson.GetBytes(out, "request.contents")
	if !contents.IsArray() || len(contents.Array()) != 1 {
		t.Fatalf("want exactly 1 remaining content (the user greeting), got: %s", out)
	}
	if got := contents.Array()[0].Get("parts.0.text").String(); got != "hi" {
		t.Fatalf("remaining content = %s, want the user greeting", out)
	}
}

// TestAntigravityDropEmptyNameFunctionCallPartsKeepsWellFormedPairs verifies
// that a well-formed call/response pair with real names is left byte-for-byte
// untouched.
func TestAntigravityDropEmptyNameFunctionCallPartsKeepsWellFormedPairs(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"Read","args":{"file_path":"/a"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"Read","response":{"result":"ok"}}}]}]}}`)

	out := antigravityDropEmptyNameFunctionCallParts(payload)

	if string(out) != string(payload) {
		t.Fatalf("well-formed payload was modified:\n got: %s\nwant: %s", out, payload)
	}
	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("well-formed payload should already pass validation: %v", err)
	}
}

// TestAntigravityDropEmptyNameFunctionCallPartsDropsWhitespaceOnlyName
// verifies that a whitespace-only functionCall.name is treated the same as
// an empty name.
func TestAntigravityDropEmptyNameFunctionCallPartsDropsWhitespaceOnlyName(t *testing.T) {
	payload := []byte(`{"request":{"contents":[
		{"role":"user","parts":[{"text":"hi"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"   ","args":{}}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"","response":{"result":"ok"}}}]}
	]}}`)

	out := antigravityDropEmptyNameFunctionCallParts(payload)

	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("validator still rejects repaired payload: %v; output=%s", err, out)
	}
	if gjson.GetBytes(out, "request.contents.1.parts.0.functionCall").Exists() {
		t.Fatalf("whitespace-only-name functionCall was not dropped: %s", out)
	}
}

// TestAntigravityDropEmptyNameFunctionCallPartsRemovesEmptiedContent verifies
// that a content left with zero parts after dropping is removed from
// request.contents entirely, rather than left behind as an empty parts array.
func TestAntigravityDropEmptyNameFunctionCallPartsRemovesEmptiedContent(t *testing.T) {
	payload := []byte(`{"request":{"contents":[
		{"role":"user","parts":[{"text":"before"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"","args":{}}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"","response":{"result":"ok"}}}]},
		{"role":"user","parts":[{"text":"after"}]}
	]}}`)

	out := antigravityDropEmptyNameFunctionCallParts(payload)

	contents := gjson.GetBytes(out, "request.contents")
	if !contents.IsArray() {
		t.Fatalf("contents missing: %s", out)
	}
	arr := contents.Array()
	if len(arr) != 2 {
		t.Fatalf("want 2 remaining contents (before/after), got %d: %s", len(arr), out)
	}
	if arr[0].Get("parts.0.text").String() != "before" || arr[1].Get("parts.0.text").String() != "after" {
		t.Fatalf("unexpected surviving contents: %s", out)
	}
	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("repaired payload should pass validation: %v", err)
	}
}

// TestAntigravityDropEmptyNameFunctionCallPartsDropsMultipleInOneContent
// verifies that several empty-name functionCall parts within a single model
// content are all dropped, and their paired empty-name functionResponse
// parts (matched positionally, mirroring the validator's own pending-slot
// pairing) are dropped too, while a real call/response pair in the same
// batch survives.
func TestAntigravityDropEmptyNameFunctionCallPartsDropsMultipleInOneContent(t *testing.T) {
	payload := []byte(`{"request":{"contents":[
		{"role":"user","parts":[{"text":"hi"}]},
		{"role":"model","parts":[
			{"functionCall":{"id":"call-1","name":"","args":{}}},
			{"functionCall":{"id":"call-2","name":"Read","args":{"file_path":"/a"}}},
			{"functionCall":{"id":"call-3","name":"","args":{}}}
		]},
		{"role":"user","parts":[
			{"functionResponse":{"id":"call-1","name":"","response":{"result":"one"}}},
			{"functionResponse":{"id":"call-2","name":"Read","response":{"result":"two"}}},
			{"functionResponse":{"id":"call-3","name":"","response":{"result":"three"}}}
		]}
	]}}`)

	out := antigravityDropEmptyNameFunctionCallParts(payload)

	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("validator still rejects repaired payload: %v; output=%s", err, out)
	}
	callParts := gjson.GetBytes(out, "request.contents.1.parts")
	if !callParts.IsArray() || len(callParts.Array()) != 1 {
		t.Fatalf("want exactly 1 surviving functionCall part, got: %s", out)
	}
	if got := callParts.Array()[0].Get("functionCall.name").String(); got != "Read" {
		t.Fatalf("surviving functionCall.name = %q, want Read: %s", got, out)
	}
	responseParts := gjson.GetBytes(out, "request.contents.2.parts")
	if !responseParts.IsArray() || len(responseParts.Array()) != 1 {
		t.Fatalf("want exactly 1 surviving functionResponse part, got: %s", out)
	}
	if got := responseParts.Array()[0].Get("functionResponse.name").String(); got != "Read" {
		t.Fatalf("surviving functionResponse.name = %q, want Read: %s", got, out)
	}
}

// TestAntigravityDropEmptyNameFunctionCallPartsDropsEmptyCallsInMixedContent
// covers the residual gap from v7.2.147-10: the validator's per-part empty-name
// check runs before its interleaved-shape check, so a content that mixes
// functionCall and functionResponse parts must still have its empty-name calls
// dropped instead of being skipped for the shape error. When the drop leaves
// only responses, they pair against the preceding pending calls like a
// responses-only content.
func TestAntigravityDropEmptyNameFunctionCallPartsDropsEmptyCallsInMixedContent(t *testing.T) {
	payload := []byte(`{"request":{"contents":[
		{"role":"user","parts":[{"text":"hi"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call-0","name":"List","args":{}}}]},
		{"role":"user","parts":[
			{"functionResponse":{"id":"call-0","name":"List","response":{"result":"ok"}}},
			{"functionCall":{"id":"call-1","name":"","args":{}}}
		]}
	]}}`)

	if err := internalsignature.ValidateGeminiFunctionCallPairing(payload); err == nil {
		t.Fatal("fixture should be rejected by the validator before repair")
	}
	if errText := internalsignature.ValidateGeminiFunctionCallPairing(payload); errText != nil && !strings.Contains(errText.Error(), "missing functionCall.name") {
		t.Fatalf("fixture should fail on the empty-name check, not the shape check: %v", errText)
	}

	out := antigravityDropEmptyNameFunctionCallParts(payload)

	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("validator still rejects repaired payload: %v; output=%s", err, out)
	}
	// The empty-name call was dropped from the mixed content; the remaining
	// response part pairs against the pending "List" call from the model
	// content before it.
	mixedParts := gjson.GetBytes(out, "request.contents.2.parts")
	if !mixedParts.IsArray() || len(mixedParts.Array()) != 1 {
		t.Fatalf("want exactly 1 surviving part in the mixed content, got: %s", out)
	}
	if got := mixedParts.Array()[0].Get("functionResponse.name").String(); got != "List" {
		t.Fatalf("surviving part = %s, want the List functionResponse", mixedParts.Array()[0].Raw)
	}
}

// TestAntigravityDropEmptyNameFunctionCallPartsDropsAllEmptyCallsInMixedContent
// verifies that a mixed content whose only calls are empty-name ones degrades
// to a responses-only content that pairs against the preceding pending calls:
// the response pairing with a preceding empty-name (dropped) call is dropped
// too, leaving nothing behind in either content.
func TestAntigravityDropEmptyNameFunctionCallPartsDropsAllEmptyCallsInMixedContent(t *testing.T) {
	payload := []byte(`{"request":{"contents":[
		{"role":"user","parts":[{"text":"hi"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"","args":{}}}]},
		{"role":"user","parts":[
			{"functionResponse":{"id":"call-1","name":"","response":{"result":"orphan"}}},
			{"functionCall":{"id":"call-2","name":"","args":{}}}
		]}
	]}}`)

	out := antigravityDropEmptyNameFunctionCallParts(payload)

	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("validator still rejects repaired payload: %v; output=%s", err, out)
	}
	contents := gjson.GetBytes(out, "request.contents")
	if !contents.IsArray() || len(contents.Array()) != 1 {
		t.Fatalf("want exactly 1 remaining content (the user greeting), got: %s", out)
	}
}

// TestAntigravityDropEmptyNameFunctionCallPartsLeavesRealMixedCallsToShapeError
// verifies that when dropping empty-name calls from a mixed content leaves real
// calls interleaved with responses, the empty-name repair stops there: the
// surviving payload must contain no empty-name call, and the validator must
// report the accurate interleaved shape error instead of the misleading
// "missing functionCall.name".
func TestAntigravityDropEmptyNameFunctionCallPartsLeavesRealMixedCallsToShapeError(t *testing.T) {
	payload := []byte(`{"request":{"contents":[
		{"role":"model","parts":[
			{"functionCall":{"id":"call-1","name":"Read","args":{}}},
			{"functionCall":{"id":"call-2","name":"","args":{}}},
			{"functionResponse":{"id":"call-1","name":"Read","response":{"result":"ok"}}}
		]}
	]}}`)

	if err := internalsignature.ValidateGeminiFunctionCallPairing(payload); err == nil || !strings.Contains(err.Error(), "missing functionCall.name") {
		t.Fatalf("fixture should fail on the empty-name check before repair, got: %v", err)
	}

	out := antigravityDropEmptyNameFunctionCallParts(payload)

	parts := gjson.GetBytes(out, "request.contents.0.parts")
	for _, part := range parts.Array() {
		if part.Get("functionCall").Exists() && strings.TrimSpace(part.Get("functionCall.name").String()) == "" {
			t.Fatalf("empty-name functionCall survived the repair: %s", out)
		}
	}
	err := internalsignature.ValidateGeminiFunctionCallPairing(out)
	if err == nil || strings.Contains(err.Error(), "missing functionCall.name") {
		t.Fatalf("want the accurate interleaved shape error after repair, got: %v", err)
	}
}

// TestPrepareAntigravityGeminiReasoningReplayPayloadDropsEmptyNameHistory is
// an end-to-end check through prepareAntigravityGeminiReasoningReplayPayload:
// polluted history that would otherwise trigger the 400 "invalid Gemini
// function call history: missing functionCall.name" must be repaired instead
// of rejected, for a model name that gates into the reasoning replay path.
func TestPrepareAntigravityGeminiReasoningReplayPayloadDropsEmptyNameHistory(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	payload := []byte(`{"sessionId":"empty-name-history","request":{"contents":[
		{"role":"user","parts":[{"text":"hi"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"","args":{}}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"","response":{"result":"ok"}}}]}
	]}}`)

	out, _, err := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3-flash-agent", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if err != nil {
		t.Fatalf("prepare returned an error instead of repairing polluted history: %v", err)
	}
	if err := internalsignature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("repaired payload still fails validation: %v; output=%s", err, out)
	}
}

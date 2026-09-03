package executor

import (
	"context"
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

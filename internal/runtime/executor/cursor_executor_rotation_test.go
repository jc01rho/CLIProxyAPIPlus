package executor

import (
	"errors"
	"testing"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
)

// Pins senpi's zero-token resource_exhausted classification at commit
// e40acbc55c44c641287972bea0432231cad85196
// (packages/ai/src/api/cursor-conversation-rotation.ts:isZeroTokenResourceExhausted,
// called from cursor-agent.ts with usageState.sawTokenDelta).
//
// senpi gates rotation on sawTokenDelta ONLY. sawTokenDelta is set exclusively by
// the tokenDelta interaction update; checkpoint and turnEnded frames never set it.
// A checkpoint arriving before a bare resource_exhausted therefore must NOT
// suppress rotation.

var errCursorResourceExhausted = &cursorproto.ConnectError{Code: "resource_exhausted", Message: "resource exhausted"}

// Given a checkpoint reported the live conversation size but no token delta ever
// streamed,
// When a bare resource_exhausted ends the stream,
// Then the turn is still classified as a zero-token RE (rotation eligible).
func TestCursorZeroTokenResourceExhaustedIgnoresCheckpointOnlyUsage(t *testing.T) {
	usage := &cursorTokenUsage{}
	applyDecodedFrame(t, usage, checkpointFrame(148_256))

	if input, _ := usage.get(); input == 0 {
		t.Fatalf("precondition: checkpoint did not populate input accounting")
	}
	if !isCursorZeroTokenResourceExhausted(errCursorResourceExhausted, usage) {
		t.Fatal("checkpoint-only usage suppressed the zero-token classification; senpi gates on sawTokenDelta only")
	}
}

// Given a token delta streamed before the failure,
// When a resource_exhausted ends the stream,
// Then it is a mid-flight context overflow, not a poisoned conversation.
func TestCursorZeroTokenResourceExhaustedRespectsStreamedTokenDelta(t *testing.T) {
	usage := &cursorTokenUsage{}
	usage.addOutput(5)

	if isCursorZeroTokenResourceExhausted(errCursorResourceExhausted, usage) {
		t.Fatal("a streamed token delta must clear the zero-token classification")
	}
}

// Given a non-resource_exhausted failure,
// When it is classified,
// Then it is never treated as a zero-token RE regardless of usage.
func TestCursorZeroTokenResourceExhaustedRequiresResourceExhausted(t *testing.T) {
	usage := &cursorTokenUsage{}

	if isCursorZeroTokenResourceExhausted(errors.New("cursor: stream error: unavailable"), usage) {
		t.Fatal("an unrelated error must not be classified as a zero-token RE")
	}
	if isCursorZeroTokenResourceExhausted(nil, usage) {
		t.Fatal("a nil error must not be classified as a zero-token RE")
	}
}

// Given a turnEnded billed split arrived but no token delta ever streamed,
// When a resource_exhausted ends the stream,
// Then senpi still classifies it as zero-token: only tokenDelta clears the gate.
func TestCursorZeroTokenResourceExhaustedIgnoresBilledTurnEnded(t *testing.T) {
	usage := &cursorTokenUsage{}
	applyDecodedFrame(t, usage, billedTurnEndedFrame(map[int]int64{
		cursorproto.TEU_InputTokens:  1_000,
		cursorproto.TEU_OutputTokens: 7,
	}))

	if !isCursorZeroTokenResourceExhausted(errCursorResourceExhausted, usage) {
		t.Fatal("billed turnEnded suppressed the zero-token classification; senpi gates on sawTokenDelta only")
	}
}

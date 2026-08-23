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

// newRotationTestExecutor builds a CursorExecutor without the cleanup-loop
// goroutine so rotation-state tests stay deterministic.
func newRotationTestExecutor() *CursorExecutor {
	return &CursorExecutor{
		sessions:    make(map[string]*cursorSession),
		checkpoints: make(map[string]*savedCheckpoint),
		rotations:   make(map[string]*cursorConversationRotation),
	}
}

// Given a base conversation with no rotation history,
// When successive zero-token resource_exhausted failures drive the rotation
// state machine,
// Then it follows senpi's lifecycle exactly (gap-matrix rows 5-6,
// cursor-conversation-rotation.ts + cursor-agent.ts:893-910):
//   - the FIRST 0-token RE is surfaced UN-ROTATED (the client can shrink an
//     oversized payload; rotation cannot fix that)
//   - subsequent 0-token REs rotate, up to the cap of 3
//   - the poison past the cap sets skip
//   - the next effectiveCursorConversation use remints a fresh wire id with
//     reset counters
func TestCursorRotationSurfaceFirstThenRotateCapAndRemint(t *testing.T) {
	e := newRotationTestExecutor()
	const base = "base-conv-1"

	if got := e.effectiveCursorConversation(base); got != base {
		t.Fatalf("effectiveCursorConversation() = %q, want the base id before any rotation", got)
	}

	// First 0-token RE: surface-first, no rotation spent.
	rotated, outcome := e.rotateOnCursorZeroTokenRE(base, base, "cursor.json")
	if outcome != cursorRotationSurface || rotated != "" {
		t.Fatalf("first zero-token RE: outcome=%v rotated=%q, want surface with no rotation", outcome, rotated)
	}
	rec := e.rotations[base]
	if rec == nil || !rec.surfaced || rec.poisonCount != 0 || rec.skip {
		t.Fatalf("first zero-token RE: record = %+v, want {surfaced:true poisonCount:0 skip:false}", rec)
	}
	if got := e.effectiveCursorConversation(base); got != base {
		t.Fatalf("after surface-first, effective wire id = %q, want the un-rotated base id", got)
	}

	// Second 0-token RE (next request on the same base): rotation is spent now.
	firstWire, outcome := e.rotateOnCursorZeroTokenRE(base, base, "cursor.json")
	if outcome != cursorRotationRotated || firstWire == "" || firstWire == base {
		t.Fatalf("second zero-token RE: outcome=%v rotated=%q, want a fresh wire id", outcome, firstWire)
	}
	if rec.poisonCount != 1 || rec.skip {
		t.Fatalf("after first rotation: record = %+v, want poisonCount=1 skip=false", rec)
	}
	if got := e.effectiveCursorConversation(base); got != firstWire {
		t.Fatalf("effective wire id = %q, want the rotated id %q", got, firstWire)
	}

	// Rotations two and three: the cap allows three total.
	wire := firstWire
	for rotation := 2; rotation <= cursorMaxConversationRotations; rotation++ {
		next, outcome := e.rotateOnCursorZeroTokenRE(base, wire, "cursor.json")
		if outcome != cursorRotationRotated || next == "" || next == wire {
			t.Fatalf("rotation %d: outcome=%v rotated=%q, want a fresh wire id", rotation, outcome, next)
		}
		if rec.poisonCount != rotation {
			t.Fatalf("rotation %d: poisonCount = %d, want %d", rotation, rec.poisonCount, rotation)
		}
		wire = next
	}

	// The poison that exceeds the cap sets skip (poisoned conversation).
	if rotated, outcome := e.rotateOnCursorZeroTokenRE(base, wire, "cursor.json"); outcome != cursorRotationPoisoned || rotated != "" {
		t.Fatalf("poison past the cap: outcome=%v rotated=%q, want poisoned with no rotation", outcome, rotated)
	}
	if !rec.skip || rec.poisonCount != cursorMaxConversationRotations {
		t.Fatalf("after cap: record = %+v, want {skip:true poisonCount:%d}", rec, cursorMaxConversationRotations)
	}

	// Post-skip use: effectiveCursorConversation remints a fresh wire id with
	// reset counters — a poisoned conversation never wedges the base id forever.
	reminted := e.effectiveCursorConversation(base)
	if reminted == "" || reminted == base || reminted == wire {
		t.Fatalf("post-skip remint = %q, want a fresh wire id distinct from base %q and poisoned wire %q", reminted, base, wire)
	}
	if rec.skip || rec.poisonCount != 0 || rec.surfaced {
		t.Fatalf("after remint: record = %+v, want {skip:false poisonCount:0 surfaced:false}", rec)
	}
	if got := e.effectiveCursorConversation(base); got != reminted {
		t.Fatalf("reminted wire id must be sticky on the next use: got %q, want %q", got, reminted)
	}
}

// Given a cached checkpoint and MCP sessions keyed by the current wire id,
// When a rotation moves the conversation to a fresh wire id,
// Then the checkpoint and the auth-matching session migrate with it (parity
// with the original rotateCursorConversation migration).
func TestCursorRotationMigratesCheckpointAndSession(t *testing.T) {
	e := newRotationTestExecutor()
	const base, wire = "base-conv-2", "wire-conv-2"
	cp := &savedCheckpoint{data: []byte("checkpoint"), authID: "cursor.json"}
	e.checkpoints[wire] = cp
	e.sessions["cursor.json:"+wire] = &cursorSession{authID: "cursor.json"}
	e.sessions["other.json:"+wire] = &cursorSession{authID: "other.json"}
	e.sessions["cursor.json:unrelated"] = &cursorSession{authID: "cursor.json"}

	_, _ = e.rotateOnCursorZeroTokenRE(base, wire, "cursor.json") // surface-first
	rotated, outcome := e.rotateOnCursorZeroTokenRE(base, wire, "cursor.json")
	if outcome != cursorRotationRotated {
		t.Fatalf("outcome = %v, want rotated", outcome)
	}

	if e.checkpoints[rotated] != cp {
		t.Fatal("checkpoint did not migrate to the rotated wire id")
	}
	if _, ok := e.checkpoints[wire]; ok {
		t.Fatal("stale checkpoint remained under the old wire id")
	}
	if e.sessions["cursor.json:"+rotated] == nil {
		t.Fatal("auth-matching MCP session did not migrate to the rotated wire id")
	}
	if _, ok := e.sessions["cursor.json:"+wire]; ok {
		t.Fatal("stale session remained under the old wire id")
	}
	if _, ok := e.sessions["other.json:"+wire]; !ok {
		t.Fatal("session belonging to a different auth must not migrate")
	}
	if _, ok := e.sessions["cursor.json:unrelated"]; !ok {
		t.Fatal("unrelated session was disturbed by the rotation")
	}
}

// Pins senpi's CURSOR_CONVERSATION_POISONED_MESSAGE exactly
// (cursor-conversation-rotation.ts:8-9).
func TestCursorConversationPoisonedMessageMatchesSenpi(t *testing.T) {
	const senpiMessage = "Cursor conversation is poisoned for this session; use another provider"
	if cursorConversationPoisonedMessage != senpiMessage {
		t.Fatalf("cursorConversationPoisonedMessage = %q, want %q", cursorConversationPoisonedMessage, senpiMessage)
	}
}

package executor

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
)

// These tests pin the senpi cursor-agent usage contract at commit
// e40acbc55c44c641287972bea0432231cad85196
// (packages/ai/src/api/cursor-agent.ts: applyBilledTurnEndedUsage /
// applyCheckpointTokenDetails, packages/ai/test/cursor-usage.test.ts).
//
// Frames are hand-encoded and pushed through the production decoder so the
// assertions cover the real wire shape, not just the Go method signatures.

// billedTurnEndedFrame encodes AgentServerMessage{interaction_update{turn_ended}}
// carrying the billed TurnEndedUpdate varint fields keyed by field number, the
// same hand-encoding senpi's cursor-usage.test.ts uses.
func billedTurnEndedFrame(fields map[int]int64) []byte {
	var turnEnded []byte
	for _, num := range []int{
		cursorproto.TEU_InputTokens,
		cursorproto.TEU_OutputTokens,
		cursorproto.TEU_CacheReadTokens,
		cursorproto.TEU_CacheWriteTokens,
		cursorproto.TEU_ReasoningTokens,
	} {
		value, ok := fields[num]
		if !ok {
			continue
		}
		turnEnded = protowire.AppendTag(turnEnded, protowire.Number(num), protowire.VarintType)
		turnEnded = protowire.AppendVarint(turnEnded, uint64(value))
	}
	interaction := protowire.AppendTag(nil, cursorproto.IU_TurnEnded, protowire.BytesType)
	interaction = protowire.AppendBytes(interaction, turnEnded)
	msg := protowire.AppendTag(nil, cursorproto.ASM_InteractionUpdate, protowire.BytesType)
	return protowire.AppendBytes(msg, interaction)
}

// checkpointFrame encodes
// AgentServerMessage{conversation_checkpoint_update{token_details{used_tokens}}}.
func checkpointFrame(usedTokens int64) []byte {
	details := protowire.AppendTag(nil, cursorproto.CTD_UsedTokens, protowire.VarintType)
	details = protowire.AppendVarint(details, uint64(usedTokens))
	state := protowire.AppendTag(nil, cursorproto.CSS_TokenDetails, protowire.BytesType)
	state = protowire.AppendBytes(state, details)
	msg := protowire.AppendTag(nil, cursorproto.ASM_ConversationCheckpoint, protowire.BytesType)
	return protowire.AppendBytes(msg, state)
}

// applyDecodedFrame routes one decoded server frame into the usage accumulator
// exactly as processH2SessionFrames does.
func applyDecodedFrame(t *testing.T, usage *cursorTokenUsage, frame []byte) {
	t.Helper()
	msg, err := cursorproto.DecodeAgentServerMessage(frame)
	if err != nil {
		t.Fatalf("DecodeAgentServerMessage() error = %v", err)
	}
	switch msg.Type {
	case cursorproto.ServerMsgCheckpoint:
		usage.applyCheckpointTokenDetails(msg.CheckpointUsedTokens)
	case cursorproto.ServerMsgTurnEnded:
		usage.applyBilledTurnEndedUsage(msg.TurnEndedInput, msg.TurnEndedOutput, msg.TurnEndedCacheRead, msg.TurnEndedCacheWrite)
	default:
		t.Fatalf("unexpected decoded frame type %v", msg.Type)
	}
}

// Given a checkpoint reporting a ~148k live window and a billed turnEnded whose
// cache_read is dashboard-cumulative (~3.99M),
// When both frames are decoded and applied,
// Then the dwarfing cache_read is discarded and the reported context stays at
// the live window instead of the inflated billed sum.
//
// Ports senpi cursor-usage.test.ts "ignores billed cacheRead that dwarfs
// checkpoint usedTokens" (session 01a01879).
func TestCursorUsageIgnoresBilledCacheReadThatDwarfsCheckpointUsedTokens(t *testing.T) {
	const liveUsed = int64(148_256)

	usage := &cursorTokenUsage{}
	usage.addOutput(5)
	applyDecodedFrame(t, usage, checkpointFrame(liveUsed))
	applyDecodedFrame(t, usage, billedTurnEndedFrame(map[int]int64{
		cursorproto.TEU_InputTokens:      4_090_000,
		cursorproto.TEU_OutputTokens:     5,
		cursorproto.TEU_CacheReadTokens:  3_990_000,
		cursorproto.TEU_CacheWriteTokens: 100,
	}))

	if usage.cacheRead != 0 {
		t.Fatalf("cacheRead = %d, want 0 (dashboard-cumulative read must be discarded)", usage.cacheRead)
	}
	if usage.cacheWrite != 100 {
		t.Fatalf("cacheWrite = %d, want 100 (cacheWrite <= liveUsed is kept)", usage.cacheWrite)
	}
	input, output := usage.get()
	if output != 5 {
		t.Fatalf("output = %d, want 5", output)
	}
	// senpi: usage.input = max(0, liveUsed - output - cacheWrite), totalTokens = liveUsed.
	if want := liveUsed - 5 - 100; input != want {
		t.Fatalf("input = %d, want %d", input, want)
	}
	if total := input + output + usage.cacheRead + usage.cacheWrite; total != liveUsed {
		t.Fatalf("total = %d, want %d (the live window, not the billed sum)", total, liveUsed)
	}
	if total := input + output; total >= 200_000 {
		t.Fatalf("reported prompt+completion = %d, want below the 200k window", total)
	}
}

// Given a billed turnEnded whose cache_read is within the live window,
// When it is decoded and applied after a checkpoint,
// Then the ordinary cache-inclusive backing-out applies and cache_read survives.
//
// Ports senpi cursor-usage.test.ts "maps billed turnEnded token fields onto
// usage" (live api2.cursor.sh second turn: 17989 = 17575 + 411 + 3).
func TestCursorUsageKeepsBilledCacheReadWithinLiveWindow(t *testing.T) {
	usage := &cursorTokenUsage{}
	usage.addOutput(5)
	applyDecodedFrame(t, usage, checkpointFrame(17_989))
	applyDecodedFrame(t, usage, billedTurnEndedFrame(map[int]int64{
		cursorproto.TEU_InputTokens:      17_989,
		cursorproto.TEU_OutputTokens:     5,
		cursorproto.TEU_CacheReadTokens:  17_575,
		cursorproto.TEU_CacheWriteTokens: 411,
		cursorproto.TEU_ReasoningTokens:  64,
	}))

	if usage.cacheRead != 17_575 {
		t.Fatalf("cacheRead = %d, want 17575", usage.cacheRead)
	}
	if usage.cacheWrite != 411 {
		t.Fatalf("cacheWrite = %d, want 411", usage.cacheWrite)
	}
	input, output := usage.get()
	if input != 3 {
		t.Fatalf("input = %d, want 3 (cache-inclusive remainder backed out)", input)
	}
	// senpi deliberately never folds reasoning_tokens (field 5) into output.
	if output != 5 {
		t.Fatalf("output = %d, want 5 (reasoning tokens must not be folded in)", output)
	}
}

// Given a checkpoint arriving mid-turn before any turnEnded,
// When it is decoded and applied,
// Then the server's live used_tokens drives in-flight input accounting.
//
// Ports senpi cursor-usage.test.ts "applies checkpoint usedTokens to in-flight
// usage". This also covers the previously dead CheckpointUsedTokens decode path.
func TestCursorUsageAppliesCheckpointUsedTokensInFlight(t *testing.T) {
	usage := &cursorTokenUsage{}
	usage.addOutput(5)
	applyDecodedFrame(t, usage, checkpointFrame(17_584))

	input, output := usage.get()
	if input != 17_584-5 {
		t.Fatalf("input = %d, want %d", input, 17_584-5)
	}
	if output != 5 {
		t.Fatalf("output = %d, want 5", output)
	}
}

// Given a checkpoint recorded a live window and the conversation was then
// rotated after a zero-token resource_exhausted,
// When the retried attempt's own billed split arrives,
// Then the previous conversation's window must not clamp it: senpi builds a
// fresh UsageState per attempt (cursor-agent.ts:542, inside the retry loop), so
// liveUsedTokens never crosses a rotation. A leaked 100-token window would make
// the rotated attempt's legitimate 19k cache_read look dashboard-cumulative and
// zero it.
func TestCursorUsageDropsLiveWindowOnConversationRotation(t *testing.T) {
	usage := &cursorTokenUsage{}
	applyDecodedFrame(t, usage, checkpointFrame(100))

	usage.resetLiveWindow()

	applyDecodedFrame(t, usage, billedTurnEndedFrame(map[int]int64{
		cursorproto.TEU_InputTokens:     20_000,
		cursorproto.TEU_OutputTokens:    5,
		cursorproto.TEU_CacheReadTokens: 19_000,
	}))

	if usage.cacheRead != 19_000 {
		t.Fatalf("cacheRead = %d, want 19000 (a stale 100-token window must not zero it)", usage.cacheRead)
	}
	if input, _ := usage.get(); input != 1_000 {
		t.Fatalf("input = %d, want 1000 (the rotated attempt's own split)", input)
	}
}

// Given a turnEnded billed split has already been applied,
// When a later checkpoint arrives,
// Then it must not override the authoritative billed numbers.
func TestCursorUsageCheckpointNeverOverridesBilledSplit(t *testing.T) {
	usage := &cursorTokenUsage{}
	applyDecodedFrame(t, usage, billedTurnEndedFrame(map[int]int64{
		cursorproto.TEU_InputTokens:      1_000,
		cursorproto.TEU_OutputTokens:     7,
		cursorproto.TEU_CacheReadTokens:  400,
		cursorproto.TEU_CacheWriteTokens: 100,
	}))
	applyDecodedFrame(t, usage, checkpointFrame(999_999))

	input, output := usage.get()
	if input != 500 {
		t.Fatalf("input = %d, want 500 (billed split is authoritative)", input)
	}
	if output != 7 {
		t.Fatalf("output = %d, want 7", output)
	}
}

package executor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"google.golang.org/protobuf/encoding/protowire"
)

// These tests drive the production server-frame dispatcher
// (processH2SessionFrames) with an in-memory transport so wire-shaped frames
// exercise the real decode + dispatch path, not just helper functions.

// fakeCursorStream is an in-memory cursorStreamConn: frames pushed into data
// are consumed exactly as H2Stream.Data delivers them, and closing data ends
// the transport the same way H2Stream's readLoop does on exit.
type fakeCursorStream struct {
	id     string
	dataCh chan []byte
	err    error

	mu     sync.Mutex
	writes [][]byte
}

func newFakeCursorStream() *fakeCursorStream {
	return &fakeCursorStream{id: "fake", dataCh: make(chan []byte, 16)}
}

func (f *fakeCursorStream) ID() string          { return f.id }
func (f *fakeCursorStream) Data() <-chan []byte { return f.dataCh }
func (f *fakeCursorStream) Err() error          { return f.err }
func (f *fakeCursorStream) Write(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	return nil
}

// deliverAndEnd queues Connect-framed AgentServerMessage payloads and then
// ends the transport, mirroring readLoop's Data-channel closure on exit.
func (f *fakeCursorStream) deliverAndEnd(payloads ...[]byte) {
	for _, payload := range payloads {
		f.dataCh <- cursorproto.FrameConnectMessage(payload, 0)
	}
	close(f.dataCh)
}

// textDeltaFrame encodes AgentServerMessage{interaction_update{text_delta{text}}}
// (TextDeltaUpdate field TDU_Text).
func textDeltaFrame(text string) []byte {
	delta := protowire.AppendTag(nil, cursorproto.TDU_Text, protowire.BytesType)
	delta = protowire.AppendString(delta, text)
	interaction := protowire.AppendTag(nil, cursorproto.IU_TextDelta, protowire.BytesType)
	interaction = protowire.AppendBytes(interaction, delta)
	msg := protowire.AppendTag(nil, cursorproto.ASM_InteractionUpdate, protowire.BytesType)
	return protowire.AppendBytes(msg, interaction)
}

// Given a turnEnded frame with no pending MCP exec,
// When it flows through the server-frame dispatcher on the normal path,
// Then the billed split (input/output/cacheRead/cacheWrite) lands on
// cursorTokenUsage.
//
// Ports gap-matrix row 1: senpi applies applyBilledTurnEndedUsage on EVERY
// turnEnded (cursor-agent.ts:3591-3594); previously the pendingMcp early-return
// skipped it, so billed usage only landed in the rare MCP-resume interleaving.
func TestCursorProcessH2SessionFramesAppliesBilledTurnEndedUsageOnNormalPath(t *testing.T) {
	tests := []struct {
		name          string
		fields        map[int]int64
		wantInput     int64
		wantOutput    int64
		wantCacheRead int64
	}{
		{
			name: "billed split with cache fields",
			fields: map[int]int64{
				cursorproto.TEU_InputTokens:      17_989,
				cursorproto.TEU_OutputTokens:     5,
				cursorproto.TEU_CacheReadTokens:  17_575,
				cursorproto.TEU_CacheWriteTokens: 411,
			},
			wantInput:     3,
			wantOutput:    5,
			wantCacheRead: 17_575,
		},
		{
			name: "cache-free billed split",
			fields: map[int]int64{
				cursorproto.TEU_InputTokens:  1_000,
				cursorproto.TEU_OutputTokens: 7,
			},
			wantInput:  1_000,
			wantOutput: 7,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := newFakeCursorStream()
			usage := &cursorTokenUsage{}
			stream.deliverAndEnd(billedTurnEndedFrame(tt.fields))

			err := processH2SessionFrames(context.Background(), stream, nil, nil, nil, nil, nil, nil, usage, nil, cursorStallWatchdog{})
			if err != nil {
				t.Fatalf("processH2SessionFrames() error = %v, want nil (turnEnded ends the normal path)", err)
			}
			input, output := usage.get()
			if input != tt.wantInput {
				t.Fatalf("input tokens = %d, want %d (billed split must land on the no-MCP path)", input, tt.wantInput)
			}
			if output != tt.wantOutput {
				t.Fatalf("output tokens = %d, want %d", output, tt.wantOutput)
			}
			if got := usage.cacheReadTokens(); got != tt.wantCacheRead {
				t.Fatalf("cacheRead tokens = %d, want %d", got, tt.wantCacheRead)
			}
		})
	}
}

// heartbeatFrame encodes AgentServerMessage{interaction_update{heartbeat{}}} —
// senpi counts server heartbeats as inbound liveness for the stall deadline
// (cursor-agent.ts:259).
func heartbeatFrame() []byte {
	interaction := protowire.AppendTag(nil, cursorproto.IU_Heartbeat, protowire.BytesType)
	interaction = protowire.AppendBytes(interaction, nil)
	msg := protowire.AppendTag(nil, cursorproto.ASM_InteractionUpdate, protowire.BytesType)
	return protowire.AppendBytes(msg, interaction)
}

// mcpExecFrame encodes AgentServerMessage{exec_server_message{id, exec_id,
// mcp_args{tool_name, tool_call_id}}} — enough for the dispatcher to park a
// pending MCP exec and keep the frame loop alive past turnEnded.
func mcpExecFrame(execMsgID uint32, execID, toolName, toolCallID string) []byte {
	mcpArgs := protowire.AppendString(protowire.AppendTag(nil, cursorproto.MCA_Name, protowire.BytesType), toolName)
	mcpArgs = protowire.AppendString(protowire.AppendTag(mcpArgs, cursorproto.MCA_ToolCallId, protowire.BytesType), toolCallID)

	esm := protowire.AppendTag(nil, cursorproto.ESM_Id, protowire.VarintType)
	esm = protowire.AppendVarint(esm, uint64(execMsgID))
	esm = protowire.AppendString(protowire.AppendTag(esm, cursorproto.ESM_ExecId, protowire.BytesType), execID)
	esm = protowire.AppendBytes(protowire.AppendTag(esm, cursorproto.ESM_McpArgs, protowire.BytesType), mcpArgs)

	msg := protowire.AppendTag(nil, cursorproto.ASM_ExecServerMessage, protowire.BytesType)
	return protowire.AppendBytes(msg, esm)
}

// manualStallWatchdog builds a deadline driven entirely by the test: every
// inbound frame increments kicks, and delivering on (or closing) fired
// simulates the deadline lapsing — no wall-clock sleeps anywhere.
func manualStallWatchdog() (cursorStallWatchdog, chan time.Time, *int) {
	fired := make(chan time.Time)
	kicks := 0
	return cursorStallWatchdog{
		fired: fired,
		kick:  func() { kicks++ },
		stop:  func() {},
	}, fired, &kicks
}

// Given an attempt where no inbound frame arrives within the stall threshold,
// When the receive select observes the deadline lapse,
// Then the attempt fails with the typed retryable stall error — never a silent
// hang waiting on a dead server.
//
// Ports gap-matrix row 3: senpi's armStreamHealthTimer with
// CURSOR_STREAM_HEALTH_FAIL_THRESHOLD_MS = 30_000 (cursor-agent.ts:258,
// :630-660). The threshold is injectable; the timer-backed subtest uses a tiny
// value so only the watchdog case is ever selectable.
func TestCursorProcessH2SessionFramesStallFailsSilentStream(t *testing.T) {
	tests := []struct {
		name    string
		frame   bool // deliver one heartbeat before the stall
		setup   func() (cursorStallWatchdog, func())
		wantMsg bool
	}{
		{
			name: "timer watchdog fires on a silent stream",
			setup: func() (cursorStallWatchdog, func()) {
				wd := newCursorStallWatchdog(25 * time.Millisecond)
				return wd, func() {}
			},
		},
		{
			name:  "deadline lapses after a heartbeat",
			frame: true,
			setup: func() (cursorStallWatchdog, func()) {
				wd, fired, _ := manualStallWatchdog()
				return wd, func() { close(fired) }
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := newFakeCursorStream()
			wd, arm := tt.setup()
			if tt.frame {
				stream.dataCh <- cursorproto.FrameConnectMessage(heartbeatFrame(), 0)
			}
			go arm()

			err := processH2SessionFrames(context.Background(), stream, nil, nil, nil, nil, nil, nil, &cursorTokenUsage{}, nil, wd)
			var retry *cursorRetryableStreamError
			if !errors.As(err, &retry) {
				t.Fatalf("processH2SessionFrames() = %T (%v), want the typed stall error", err, err)
			}
			if retry.RetryCause() != "stall" {
				t.Fatalf("retry cause = %q, want stall", retry.RetryCause())
			}
		})
	}
}

// Given inbound frames keep arriving (heartbeats included),
// When each Data() delivery lands,
// Then the stall deadline is reset for every frame — the watchdog only fails
// an attempt once inbound frames actually stop.
//
// Ports gap-matrix row 3 (liveness half): senpi resets lastInboundFrameAt on
// every h2 data chunk (cursor-agent.ts:714).
func TestCursorProcessH2SessionFramesInboundFrameResetsStallDeadline(t *testing.T) {
	stream := newFakeCursorStream()
	wd, _, kicks := manualStallWatchdog()
	stream.deliverAndEnd(heartbeatFrame(), billedTurnEndedFrame(nil))

	err := processH2SessionFrames(context.Background(), stream, nil, nil, nil, nil, nil, nil, &cursorTokenUsage{}, nil, wd)
	if err != nil {
		t.Fatalf("processH2SessionFrames() = %v, want nil (turnEnded ends the turn)", err)
	}
	if *kicks != 2 {
		t.Fatalf("deadline resets = %d, want 2 (one per inbound frame: heartbeat + turnEnded)", *kicks)
	}
}

// Given turnEnded arrived while an MCP exec is still pending,
// When a (stale) watchdog fire lands during the client-driven tool-result wait,
// Then it is ignored — the stall deadline ended with the turn, so a slow proxy
// client can never fail a completed turn with a bogus stall error.
//
// Ports gap-matrix row 3 (disarm half): senpi stops the health timer once
// turnEnded is seen (cursor-agent.ts:641-644).
func TestCursorProcessH2SessionFramesStallFireIgnoredAfterTurnEnded(t *testing.T) {
	stream := newFakeCursorStream()
	wd, fired, _ := manualStallWatchdog()

	mcpSeen := make(chan pendingMcpExec, 1)
	afterTurnEnded := make(chan string, 1)
	toolResultCh := make(chan []toolResultInfo, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- processH2SessionFrames(ctx, stream, nil, nil,
			func(text string, isThinking bool) {
				if !isThinking {
					afterTurnEnded <- text
				}
			},
			nil,
			func(exec pendingMcpExec) { mcpSeen <- exec },
			toolResultCh,
			&cursorTokenUsage{},
			nil,
			wd)
	}()

	// 1. Park the pending MCP exec (keeps the frame loop alive past turnEnded).
	stream.dataCh <- cursorproto.FrameConnectMessage(mcpExecFrame(1, "exec-1", "probe-tool", "call-1"), 0)
	exec := <-mcpSeen
	if exec.ToolCallId != "call-1" {
		t.Fatalf("pending MCP exec tool call id = %q, want call-1", exec.ToolCallId)
	}
	// 2. turnEnded followed by a text delta in the same chunk: the delta's
	//    callback proves turnEnded was processed before the stale fire below.
	stream.dataCh <- cursorproto.FrameConnectMessage(billedTurnEndedFrame(nil), 0)
	stream.dataCh <- cursorproto.FrameConnectMessage(textDeltaFrame("after"), 0)
	if text := <-afterTurnEnded; text != "after" {
		t.Fatalf("post-turnEnded text = %q, want after", text)
	}
	// 3. Deliver the stale watchdog fire on an unbuffered channel: the send
	//    only completes once the dispatcher has received it, so a missing
	//    turnEnded guard would surface the stall error right here.
	fired <- time.Now()
	// 4. Only cancellation may end the wait now.
	cancel()

	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("processH2SessionFrames() = %v, want context.Canceled (the stale fire after turnEnded must be ignored)", err)
	}
	var retry *cursorRetryableStreamError
	if errors.As(err, &retry) {
		t.Fatalf("stale watchdog fire after turnEnded surfaced as retry cause %q", retry.RetryCause())
	}
}

// Given a failed attempt whose cause is stall/clean-end/transport,
// When it is classified for the pre-turnEnded retry loop,
// Then only those causes are retryable — Connect-level errors belong to the
// rotation/conductor paths and cancellation is never retried.
//
// Ports gap-matrix row 4 (predicate half): senpi retries only
// CursorRetryableStreamError (stream-retry.ts) with transport causes mapped
// from h2 session failures (cursor-agent.ts:567-588).
func TestCursorStreamFailureClassificationForRetry(t *testing.T) {
	stall := &cursorRetryableStreamError{msg: "stalled", cause: cursorStreamRetryCauseStall}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "typed stall", err: stall, want: true},
		{name: "typed clean-end", err: &cursorRetryableStreamError{msg: "ended", cause: cursorStreamRetryCauseCleanEnd}, want: true},
		{name: "typed transport", err: &cursorRetryableStreamError{msg: "goaway", cause: cursorStreamRetryCauseTransport}, want: true},
		{name: "raw RST_STREAM transport error", err: errors.New("h2: RST_STREAM code=2"), want: true},
		{name: "raw GOAWAY transport error", err: errors.New("h2: GOAWAY code=0"), want: true},
		{name: "raw dial failure", err: errors.New("h2: TLS dial failed: connection refused"), want: true},
		{name: "connect-level resource_exhausted", err: &cursorproto.ConnectError{Code: "resource_exhausted", Message: "too big"}, want: false},
		{name: "connect-level unauthenticated", err: &cursorproto.ConnectError{Code: "unauthenticated", Message: "no"}, want: false},
		{name: "cancellation", err: context.Canceled, want: false},
		{name: "deadline", err: context.DeadlineExceeded, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCursorRetryableStreamFailure(tt.err); got != tt.want {
				t.Fatalf("isCursorRetryableStreamFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// Given the senpi backoff schedule (1s x2^n capped at 60s, +20% jitter),
// When retry delays are computed,
// Then they match cursorStreamRetryDelayMs (stream-retry.ts:17-30) exactly for
// pinned random values.
func TestCursorStreamRetryDelayMatchesSenpi(t *testing.T) {
	tests := []struct {
		name   string
		retry  int
		random float64
		want   time.Duration
	}{
		{name: "first retry base", retry: 0, random: 0, want: 1 * time.Second},
		{name: "first retry half jitter", retry: 0, random: 0.5, want: 1100 * time.Millisecond},
		{name: "first retry full jitter", retry: 0, random: 1, want: 1200 * time.Millisecond},
		{name: "fourth retry", retry: 3, random: 0, want: 8 * time.Second},
		{name: "capped at 60s", retry: 6, random: 0, want: 60 * time.Second},
		{name: "cap applies before jitter", retry: 7, random: 0.25, want: 63 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cursorStreamRetryDelay(tt.retry, func() float64 { return tt.random })
			if got != tt.want {
				t.Fatalf("cursorStreamRetryDelay(%d, %.2f) = %v, want %v", tt.retry, tt.random, got, tt.want)
			}
		})
	}
}

// instantRetryPolicy removes wall-clock from the retry loop: zero delay and a
// wait that reports only ctx cancellation.
func instantRetryPolicy(maxRetries int) cursorStreamRetryPolicy {
	return cursorStreamRetryPolicy{
		MaxRetries: maxRetries,
		DelayFor:   func(int) time.Duration { return 0 },
		Wait:       func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	}
}

// Given a turn attempt wrapped in the bounded pre-turnEnded retry loop,
// When failures and aborts are scripted per attempt,
// Then the loop follows senpi exactly (gap-matrix row 4,
// cursor-agent.ts:826-860 + stream-retry.ts):
//   - stall/clean-end/transport failures retry with backoff; success stops;
//   - a retry after a checkpoint-saving attempt sends a Resume action
//     (forceResume) instead of re-sending the user message;
//   - the budget is the initial attempt plus at most 10 retries;
//   - abort during backoff surfaces the attempt failure immediately;
//   - zero-token resource_exhausted defers to the rotation hook, and rotation
//     retries share the same loop and budget (one streamRetries counter).
func TestCursorStreamAttemptLoop(t *testing.T) {
	stall := func() error {
		return &cursorRetryableStreamError{msg: "Cursor stream ended before turnEnded: inbound stream stalled", cause: cursorStreamRetryCauseStall}
	}
	cleanEnd := &cursorRetryableStreamError{msg: "Cursor stream ended before turnEnded", cause: cursorStreamRetryCauseCleanEnd}
	transportErr := errors.New("h2: RST_STREAM code=2")
	zeroTokenRE := &cursorproto.ConnectError{Code: "resource_exhausted", Message: "resource exhausted"}

	tests := []struct {
		name             string
		maxRetries       int
		results          []error // per-attempt result; index >= len repeats the last entry
		sawCheckpoint    func(attempt int) bool
		rotate           func(attempt int, err error) bool
		abortDuringWait  bool
		wantAttempts     int
		wantForceResume  []bool
		wantErrIs        error
		wantRotationSeen int
	}{
		{
			name:            "stall retries until success",
			maxRetries:      10,
			results:         []error{stall(), stall(), nil},
			wantAttempts:    3,
			wantForceResume: []bool{false, false, false},
		},
		{
			name:            "checkpoint on failed attempt forces Resume action",
			maxRetries:      10,
			results:         []error{stall(), nil},
			sawCheckpoint:   func(attempt int) bool { return attempt == 0 },
			wantAttempts:    2,
			wantForceResume: []bool{false, true},
		},
		{
			name:            "budget is initial attempt plus ten retries",
			maxRetries:      10,
			results:         []error{stall()},
			wantAttempts:    11,
			wantForceResume: []bool{false, false, false, false, false, false, false, false, false, false, false},
			wantErrIs:       nil, // asserted via typed check below
			// The budget-exhausted stall still passes senpi's rotation check;
			// it is consulted once and declines (stall is not a 0-token RE).
			wantRotationSeen: 1,
		},
		{
			name:            "abort during backoff stops the loop",
			maxRetries:      10,
			results:         []error{stall()},
			abortDuringWait: true,
			wantAttempts:    1,
			wantForceResume: []bool{false},
		},
		{
			name:            "clean-end and raw transport failures retry",
			maxRetries:      10,
			results:         []error{cleanEnd, transportErr, nil},
			wantAttempts:    3,
			wantForceResume: []bool{false, false, false},
		},
		{
			name:             "zero-token resource_exhausted defers to rotation hook",
			maxRetries:       10,
			results:          []error{zeroTokenRE},
			rotate:           func(attempt int, err error) bool { return false },
			wantAttempts:     1,
			wantForceResume:  []bool{false},
			wantErrIs:        zeroTokenRE,
			wantRotationSeen: 1,
		},
		{
			name:       "rotation retries share the stall budget",
			maxRetries: 1,
			// stall (retry, budget spent) -> zero-token RE (rotate, continue) ->
			// stall (budget exhausted, rotation declines) -> surfaced.
			results:         []error{stall(), zeroTokenRE, stall()},
			rotate:          func(attempt int, err error) bool { return attempt == 1 },
			wantAttempts:    3,
			wantForceResume: []bool{false, false, false},
			wantErrIs:       nil,
			// Rotation consulted for the mid-loop RE (accepted) and for the final
			// budget-exhausted stall (declined) — senpi's single catch block.
			wantRotationSeen: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			policy := instantRetryPolicy(tt.maxRetries)
			if tt.abortDuringWait {
				// Abort lands inside the backoff wait: the wait cancels ctx and
				// reports it; the loop must surface the attempt failure without
				// opening a second attempt.
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				wait := policy.Wait
				policy.Wait = func(c context.Context, d time.Duration) error {
					cancel()
					return wait(c, d)
				}
			}

			attempts := 0
			var forceResumeSeq []bool
			rotationSeen := 0
			resultAt := func(i int) error {
				if i < len(tt.results) {
					return tt.results[i]
				}
				return tt.results[len(tt.results)-1]
			}
			err := runCursorStreamAttempts(ctx, policy,
				func(forceResume bool) error {
					forceResumeSeq = append(forceResumeSeq, forceResume)
					defer func() { attempts++ }()
					return resultAt(attempts)
				},
				func() bool {
					if tt.sawCheckpoint == nil {
						return false
					}
					return tt.sawCheckpoint(attempts - 1)
				},
				func(err error) bool {
					rotationSeen++
					if tt.rotate == nil {
						return false
					}
					return tt.rotate(attempts-1, err)
				})

			if attempts != tt.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts, tt.wantAttempts)
			}
			if len(forceResumeSeq) != len(tt.wantForceResume) || len(forceResumeSeq) != attempts {
				t.Fatalf("forceResume sequence %v, want %v over %d attempts", forceResumeSeq, tt.wantForceResume, attempts)
			}
			for i, got := range forceResumeSeq {
				if got != tt.wantForceResume[i] {
					t.Fatalf("forceResume[%d] = %v, want %v (seq %v)", i, got, tt.wantForceResume[i], forceResumeSeq)
				}
			}
			if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("runCursorStreamAttempts() = %v, want %v", err, tt.wantErrIs)
			}
			if rotationSeen != tt.wantRotationSeen {
				t.Fatalf("rotation hook calls = %d, want %d", rotationSeen, tt.wantRotationSeen)
			}
			if tt.name == "budget is initial attempt plus ten retries" {
				var retry *cursorRetryableStreamError
				if !errors.As(err, &retry) || retry.RetryCause() != "stall" {
					t.Fatalf("exhausted loop returned %v, want the final stall error", err)
				}
			}
			if tt.name == "rotation retries share the stall budget" {
				var retry *cursorRetryableStreamError
				if !errors.As(err, &retry) || retry.RetryCause() != "stall" {
					t.Fatalf("post-rotation stall returned %v, want the surfaced stall error", err)
				}
			}
		})
	}
}

// Given buildRunRequestParams computes the Run action,
// When forceResume is set by the retry loop after a checkpoint was saved,
// Then the request carries ResumeAction instead of re-sending the user
// message — even though an active user message is present.
//
// Ports gap-matrix row 4 (resume half): senpi's
// forceResumeAction ||= attemptSawCheckpoint gate (cursor-agent.ts:846,
// :4213-4226).
func TestCursorBuildRunRequestParamsForceResumeOverridesUserMessage(t *testing.T) {
	parsed := &parsedOpenAIRequest{
		Model:           "composer-2.5",
		SystemPrompt:    "You are a helpful assistant.",
		UserText:        "hello",
		ActiveUserIndex: 0,
	}

	if params := buildRunRequestParams(parsed, "conv-1", false); params.Resume {
		t.Fatal("Resume = true without forceResume; an active user message must send UserMessageAction")
	}
	if params := buildRunRequestParams(parsed, "conv-1", true); !params.Resume {
		t.Fatal("Resume = false with forceResume; a post-checkpoint retry must send ResumeAction")
	}

	// Without an active user message the request is a Resume regardless.
	noUser := &parsedOpenAIRequest{Model: "composer-2.5", ActiveUserIndex: -1}
	if params := buildRunRequestParams(noUser, "conv-1", false); !params.Resume {
		t.Fatal("Resume = false without an active user message")
	}
}

// Given the same session id but a changed system prompt,
// When the conversation identity is derived,
// Then the conversation id changes — a prompt change must not reuse the stale
// checkpoint (todos/fileStates/summaries) keyed under the old identity.
//
// Ports gap-matrix row 9: senpi invalidates cached ConversationStateStructure
// whenever the system-prompt blob prefix no longer matches
// (cursor-agent.ts:4241-4263); hashing the prompt into the conversation key
// gives the same no-stale-reuse outcome.
func TestCursorDeriveConversationIdInvalidatesOnSystemPromptChange(t *testing.T) {
	base := deriveConversationId("client-key", "session-1", "prompt A")
	if changed := deriveConversationId("client-key", "session-1", "prompt B"); changed == base {
		t.Fatal("conversation id survived a system-prompt change; the stale checkpoint would be reused")
	}
	if again := deriveConversationId("client-key", "session-1", "prompt A"); again != base {
		t.Fatalf("conversation id for an unchanged prompt is not stable: %q vs %q", again, base)
	}
	if other := deriveConversationId("client-key-2", "session-1", "prompt A"); other == base {
		t.Fatal("conversation ids collided across api keys")
	}
}

// Given the stall watchdog deadline,
// When a non-positive timeout is supplied,
// Then the 30s senpi default applies.
func TestNewCursorStallWatchdogDefaultsToSenpiThreshold(t *testing.T) {
	if cursorStreamStallThreshold != 30*time.Second {
		t.Fatalf("cursorStreamStallThreshold = %v, want 30s (CURSOR_STREAM_HEALTH_FAIL_THRESHOLD_MS)", cursorStreamStallThreshold)
	}
	wd := newCursorStallWatchdog(0)
	select {
	case <-wd.fired:
		t.Fatal("watchdog fired immediately under the default threshold")
	default:
	}
	wd.disarm()
}

// Given a stream that ends cleanly (Data closed, no transport error) before
// any turnEnded frame,
// When the dispatcher observes the end,
// Then it returns the typed retryable clean-end error — not nil, which would
// report a truncated turn as a successful response.
//
// Ports gap-matrix row 2: senpi's "Cursor stream ended before turnEnded"
// clean-end failure (cursor-agent.ts:770-774, stream-retry.ts).
func TestCursorProcessH2SessionFramesCleanEndBeforeTurnEndedIsTypedRetryableError(t *testing.T) {
	transportErr := errors.New("h2: RST_STREAM code=2")
	tests := []struct {
		name      string
		frames    [][]byte
		streamErr error
		wantCause string // "" means the error is NOT the typed retryable error
	}{
		{
			name:      "no frames at all",
			wantCause: "clean-end",
		},
		{
			name:      "text delta streamed but no turnEnded",
			frames:    [][]byte{textDeltaFrame("partial answer")},
			wantCause: "clean-end",
		},
		{
			name:      "transport error passes through unchanged",
			streamErr: transportErr,
			wantCause: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := newFakeCursorStream()
			stream.err = tt.streamErr
			if tt.streamErr != nil {
				close(stream.dataCh)
			} else {
				stream.deliverAndEnd(tt.frames...)
			}

			err := processH2SessionFrames(context.Background(), stream, nil, nil, nil, nil, nil, nil, &cursorTokenUsage{}, nil, cursorStallWatchdog{})
			if err == nil {
				t.Fatal("processH2SessionFrames() = nil for a stream that ended before turnEnded; a truncated turn must not be a success")
			}
			var retry *cursorRetryableStreamError
			if tt.wantCause != "" {
				if !errors.As(err, &retry) {
					t.Fatalf("processH2SessionFrames() = %T (%v), want *cursorRetryableStreamError", err, err)
				}
				if retry.RetryCause() != tt.wantCause {
					t.Fatalf("retry cause = %q, want %q", retry.RetryCause(), tt.wantCause)
				}
				return
			}
			if errors.As(err, &retry) {
				t.Fatalf("transport error was re-typed as retryable cause %q; want passthrough", retry.RetryCause())
			}
			if !errors.Is(err, tt.streamErr) {
				t.Fatalf("processH2SessionFrames() = %v, want the transport error passthrough", err)
			}
		})
	}
}

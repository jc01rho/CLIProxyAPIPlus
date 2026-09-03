package executor

// Red-first contract tests for the CommandCode npm 1.6.0 NDJSON stream
// protocol, as implemented by the installed command-code@1.6.0 consumeStream
// (dist/cli.mjs). The npm parser:
//   - recognizes text-delta, reasoning-start/delta/end, tool-call,
//     tool-result, finish, error, and abort events;
//   - surfaces provider-executed tool-result output as visible content;
//   - keeps reasoning hidden (internal thinking state only);
//   - normalizes finishReason: "tool-calls" -> "tool_calls",
//     "length" -> "max_tokens", otherwise "end_turn";
//   - reads totalUsage cacheRead/cacheWrite tokens (inputTokenDetails) and
//     top-level systemPromptTokens from the finish event;
//   - skips NDJSON lines that fail JSON.parse (try/catch -> continue)
//     instead of aborting the stream;
//   - throws a typed status error on an error event
//     (status = error.statusCode ?? 500), including the string-shaped form
//     {"type":"error","error":"<message>"} (status defaults to 500);
//   - treats abort as a normal (non-error) terminal event;
//   - throws a retryable 502 transport error when the stream ends without a
//     finish or abort event (truncation).
//
// Stream-surface note: the OpenAI passthrough stream translator strips the
// "data:" prefix and drops "[DONE]" lines, so a StreamChunk for [DONE] is
// never observable in these tests. The success finish chunk is the observable
// proxy for [DONE]: the implementation emits them together, so "no success
// finish chunk" pins "no [DONE]".

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// NDJSON event fixtures mirroring the npm 1.6.0 wire shapes.
const (
	ccEvText1         = `{"type":"text-delta","text":"Hello "}`
	ccEvText2         = `{"type":"text-delta","text":"world"}`
	ccEvToolCall      = `{"type":"tool-call","toolCallId":"call_abc123","toolName":"vision_lookup","input":{"query":"sparrow","limit":3}}`
	ccEvToolResult    = `{"type":"tool-result","toolCallId":"call_abc123","toolName":"vision_lookup","output":"found 3 results for sparrow"}`
	ccEvReasoningOpen = `{"type":"reasoning-start"}`
	ccEvReasoningText = `{"type":"reasoning-delta","text":"secret chain of thought"}`
	ccEvReasoningEnd  = `{"type":"reasoning-end"}`
	ccEvFinishStop    = `{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":120,"outputTokens":30}}`
	ccEvFinishTools   = `{"type":"finish","finishReason":"tool-calls","totalUsage":{"inputTokens":120,"outputTokens":30}}`
	ccEvFinishLength  = `{"type":"finish","finishReason":"length","totalUsage":{"inputTokens":120,"outputTokens":30}}`
	ccEvFinishRich    = `{"type":"finish","finishReason":"stop","systemPromptTokens":90,"totalUsage":{"inputTokens":1200,"outputTokens":300,"inputTokenDetails":{"cacheReadTokens":800,"cacheWriteTokens":150}}}`
	ccEvError429      = `{"type":"error","error":{"message":"quota exceeded","statusCode":429,"isRetryable":true}}`
	ccEvErrorString   = `{"type":"error","error":"provider exploded mid-stream"}`
	ccEvAbort         = `{"type":"abort"}`
)

func commandCodeNDJSON(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// commandCodeStreamTestContext injects a RoundTripper that answers the
// CommandCode generate request with the given NDJSON body.
func commandCodeStreamTestContext(body string) context.Context {
	return context.WithValue(context.Background(), "cliproxy.roundtripper", commandCodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}))
}

func executeCommandCodeNDJSON(t *testing.T, body string) (cliproxyexecutor.Response, error) {
	t.Helper()
	exec := NewCommandCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "user_test"}}
	payload := []byte(`{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"hello"}]}`)
	return exec.Execute(commandCodeStreamTestContext(body), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-pro",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
}

func streamCommandCodeNDJSON(t *testing.T, body string) []cliproxyexecutor.StreamChunk {
	t.Helper()
	exec := NewCommandCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "user_test"}}
	payload := []byte(`{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"hello"}]}`)
	result, err := exec.ExecuteStream(commandCodeStreamTestContext(body), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-pro",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range result.Chunks {
		chunks = append(chunks, chunk)
	}
	return chunks
}

// commandCodeErrChunks returns the errors carried by error chunks.
func commandCodeErrChunks(chunks []cliproxyexecutor.StreamChunk) []error {
	var errs []error
	for _, chunk := range chunks {
		if chunk.Err != nil {
			errs = append(errs, chunk.Err)
		}
	}
	return errs
}

// commandCodeFinishChunks returns chunk payloads whose choices[0].finish_reason
// is a non-empty string (success terminal chunks).
func commandCodeFinishChunks(chunks []cliproxyexecutor.StreamChunk) []gjson.Result {
	var finishes []gjson.Result
	for _, chunk := range chunks {
		if chunk.Err != nil || len(chunk.Payload) == 0 {
			continue
		}
		fr := gjson.GetBytes(chunk.Payload, "choices.0.finish_reason")
		if fr.Type == gjson.String && fr.String() != "" {
			finishes = append(finishes, gjson.ParseBytes(chunk.Payload))
		}
	}
	return finishes
}

// commandCodeStreamedText concatenates all delta content emitted by chunks.
func commandCodeStreamedText(chunks []cliproxyexecutor.StreamChunk) string {
	var b strings.Builder
	for _, chunk := range chunks {
		if chunk.Err != nil || len(chunk.Payload) == 0 {
			continue
		}
		b.WriteString(gjson.GetBytes(chunk.Payload, "choices.0.delta.content").String())
	}
	return b.String()
}

// commandCodeUsagePayload returns the first chunk payload carrying a usage
// object, failing the test when none does.
func commandCodeUsagePayload(t *testing.T, chunks []cliproxyexecutor.StreamChunk) gjson.Result {
	t.Helper()
	for _, chunk := range chunks {
		if chunk.Err != nil || len(chunk.Payload) == 0 {
			continue
		}
		if usage := gjson.GetBytes(chunk.Payload, "usage"); usage.IsObject() {
			return usage
		}
	}
	t.Fatalf("no stream chunk carried a usage object")
	return gjson.Result{}
}

// requireCommandCodeStatusError asserts err carries a typed status error with
// the expected HTTP-like status code, as required for retry/cooldown handling.
func requireCommandCodeStatusError(t *testing.T, err error, wantCode int) {
	t.Helper()
	var statusErr cliproxyexecutor.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error %v does not expose a StatusCode() for retry/cooldown handling", err)
	}
	if got := statusErr.StatusCode(); got != wantCode {
		t.Fatalf("StatusCode() = %d, want %d (err=%v)", got, wantCode, err)
	}
}

// --- Non-stream (Execute) contract ---

func Test_CommandCodeStream_Execute_text_toolcall_finish_produces_openai_content(t *testing.T) {
	// Given an NDJSON stream with text deltas, one tool call, and a finish event.
	body := commandCodeNDJSON(ccEvText1, ccEvText2, ccEvToolCall, ccEvFinishTools)

	// When
	response, err := executeCommandCodeNDJSON(t, body)

	// Then the OpenAI-equivalent message carries the full text and the tool
	// call with its upstream ID, name, and JSON arguments.
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != "Hello world" {
		t.Fatalf("message content = %q, want %q", got, "Hello world")
	}
	toolCalls := gjson.GetBytes(response.Payload, "choices.0.message.tool_calls")
	if !toolCalls.IsArray() || len(toolCalls.Array()) != 1 {
		t.Fatalf("message tool_calls = %s, want exactly one tool call", toolCalls.Raw)
	}
	call := toolCalls.Array()[0]
	if got := call.Get("id").String(); got != "call_abc123" {
		t.Fatalf("tool_calls[0].id = %q, want %q", got, "call_abc123")
	}
	if got := call.Get("function.name").String(); got != "vision_lookup" {
		t.Fatalf("tool_calls[0].function.name = %q, want %q", got, "vision_lookup")
	}
	arguments := call.Get("function.arguments").String()
	if got := gjson.GetBytes([]byte(arguments), "query").String(); got != "sparrow" {
		t.Fatalf("tool_calls[0].function.arguments query = %q, want %q (raw=%s)", got, "sparrow", arguments)
	}
	if got := gjson.GetBytes([]byte(arguments), "limit").Int(); got != 3 {
		t.Fatalf("tool_calls[0].function.arguments limit = %d, want %d (raw=%s)", got, 3, arguments)
	}
}

func Test_CommandCodeStream_Execute_normalizes_finish_reason_tool_calls(t *testing.T) {
	// Given a finish event whose raw finishReason is the npm "tool-calls".
	body := commandCodeNDJSON(ccEvText1, ccEvToolCall, ccEvFinishTools)

	// When
	response, err := executeCommandCodeNDJSON(t, body)

	// Then the finish reason is normalized to the npm stop reason tool_calls.
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want npm-normalized %q", got, "tool_calls")
	}
}

func Test_CommandCodeStream_Execute_normalizes_finish_reason_length_to_max_tokens(t *testing.T) {
	// Given a finish event whose raw finishReason is the npm "length".
	body := commandCodeNDJSON(ccEvText1, ccEvFinishLength)

	// When
	response, err := executeCommandCodeNDJSON(t, body)

	// Then the finish reason is normalized to the npm stop reason max_tokens.
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.finish_reason").String(); got != "max_tokens" {
		t.Fatalf("finish_reason = %q, want npm-normalized %q", got, "max_tokens")
	}
}

func Test_CommandCodeStream_Execute_tool_result_output_is_surfaced(t *testing.T) {
	// Given a provider-executed tool-result event between tool call and finish.
	// npm keeps server tool results in the visible message content.
	body := commandCodeNDJSON(ccEvText1, ccEvText2, ccEvToolCall, ccEvToolResult, ccEvFinishTools)

	// When
	response, err := executeCommandCodeNDJSON(t, body)

	// Then the tool result output text is not silently dropped.
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	content := gjson.GetBytes(response.Payload, "choices.0.message.content").String()
	if !strings.Contains(content, "Hello world") {
		t.Fatalf("message content = %q, want it to keep the assistant text %q", content, "Hello world")
	}
	if !strings.Contains(content, "found 3 results for sparrow") {
		t.Fatalf("message content = %q, want it to surface the tool-result output %q", content, "found 3 results for sparrow")
	}
}

func Test_CommandCodeStream_Execute_exposes_reasoning_content(t *testing.T) {
	// Given reasoning events wrapping the visible text.
	body := commandCodeNDJSON(ccEvReasoningOpen, ccEvReasoningText, ccEvReasoningEnd, ccEvText1, ccEvFinishStop)

	// When
	response, err := executeCommandCodeNDJSON(t, body)

	// Then visible text stays in content and reasoning is exposed as
	// reasoning_content (the OpenAI-compatible equivalent of thinking_delta).
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	content := gjson.GetBytes(response.Payload, "choices.0.message.content").String()
	if content != "Hello " {
		t.Fatalf("message content = %q, want only the visible text %q", content, "Hello ")
	}
	reasoning := gjson.GetBytes(response.Payload, "choices.0.message.reasoning_content").String()
	if reasoning != "secret chain of thought" {
		t.Fatalf("message reasoning_content = %q, want the reasoning text", reasoning)
	}
}

func Test_CommandCodeStream_Execute_parses_cache_and_system_prompt_usage(t *testing.T) {
	// Given a finish event carrying cache token details and system prompt tokens.
	body := commandCodeNDJSON(ccEvText1, ccEvFinishRich)

	// When
	response, err := executeCommandCodeNDJSON(t, body)

	// Then input/output totals stay exact (cache tokens are a subset, not
	// additive, and systemPromptTokens never corrupt totals), and the cache
	// read/write tokens are parsed into the OpenAI-compatible usage details.
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	usage := gjson.GetBytes(response.Payload, "usage")
	if got := usage.Get("prompt_tokens").Int(); got != 1200 {
		t.Fatalf("usage.prompt_tokens = %d, want %d", got, 1200)
	}
	if got := usage.Get("completion_tokens").Int(); got != 300 {
		t.Fatalf("usage.completion_tokens = %d, want %d", got, 300)
	}
	if got := usage.Get("total_tokens").Int(); got != 1500 {
		t.Fatalf("usage.total_tokens = %d, want %d", got, 1500)
	}
	if got := usage.Get("prompt_tokens_details.cached_tokens").Int(); got != 800 {
		t.Fatalf("usage.prompt_tokens_details.cached_tokens = %d, want cacheReadTokens %d", got, 800)
	}
	if got := usage.Get("prompt_tokens_details.cached_creation_tokens").Int(); got != 150 {
		t.Fatalf("usage.prompt_tokens_details.cached_creation_tokens = %d, want cacheWriteTokens %d", got, 150)
	}
}

func Test_CommandCodeStream_Execute_error_event_returns_status_error(t *testing.T) {
	// Given an upstream error event followed by a finish line; npm throws on
	// the error event and never reaches the finish.
	body := commandCodeNDJSON(ccEvText1, ccEvError429, ccEvFinishStop)

	// When
	_, err := executeCommandCodeNDJSON(t, body)

	// Then Execute returns a typed 429 error carrying the upstream message.
	if err == nil {
		t.Fatalf("Execute() error = nil, want the upstream error event surfaced as an error")
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("Execute() error = %q, want it to carry the upstream message %q", err.Error(), "quota exceeded")
	}
	requireCommandCodeStatusError(t, err, http.StatusTooManyRequests)
}

func Test_CommandCodeStream_Execute_eof_without_terminal_is_truncation(t *testing.T) {
	// Given a stream that ends after text without any finish or abort event;
	// npm treats this as a retryable 502 truncation.
	body := commandCodeNDJSON(ccEvText1, ccEvText2)

	// When
	_, err := executeCommandCodeNDJSON(t, body)

	// Then Execute returns a typed 502 truncation error instead of a
	// false-success partial response.
	if err == nil {
		t.Fatalf("Execute() error = nil, want a truncation error for EOF without finish/abort")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "truncat") {
		t.Fatalf("Execute() error = %q, want a truncation message", err.Error())
	}
	requireCommandCodeStatusError(t, err, http.StatusBadGateway)
}

func Test_CommandCodeStream_Execute_skips_malformed_ndjson_lines(t *testing.T) {
	// Given an NDJSON stream with malformed lines interleaved between valid
	// events; npm consumeStream skips lines whose JSON.parse fails instead of
	// aborting the stream.
	body := commandCodeNDJSON(ccEvText1, `{not-json-at-all`, `{"type":"text-delta","text":`, ccEvText2, ccEvFinishStop)

	// When
	response, err := executeCommandCodeNDJSON(t, body)

	// Then Execute succeeds: the malformed lines are skipped, the valid text
	// is preserved, and the finish event still terminates the stream.
	if err != nil {
		t.Fatalf("Execute() error = %v, want malformed NDJSON lines skipped, not fatal", err)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != "Hello world" {
		t.Fatalf("message content = %q, want %q", got, "Hello world")
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, want %q", got, "stop")
	}
}

func Test_CommandCodeStream_Execute_string_error_event_returns_status_error(t *testing.T) {
	// Given an error event whose payload is a quoted string (npm
	// readStreamErrorEvent: string payload -> message, statusCode undefined).
	body := commandCodeNDJSON(ccEvText1, ccEvErrorString, ccEvFinishStop)

	// When
	_, err := executeCommandCodeNDJSON(t, body)

	// Then Execute surfaces the string payload as a terminal error with the
	// npm default status 500.
	if err == nil {
		t.Fatalf("Execute() error = nil, want the string-shaped error event surfaced as a terminal error")
	}
	if !strings.Contains(err.Error(), "provider exploded mid-stream") {
		t.Fatalf("Execute() error = %q, want it to carry the upstream message %q", err.Error(), "provider exploded mid-stream")
	}
	requireCommandCodeStatusError(t, err, http.StatusInternalServerError)
}

// --- Stream (ExecuteStream) contract ---

func Test_CommandCodeStream_ExecuteStream_text_toolcall_finish_produces_openai_chunks(t *testing.T) {
	// Given an NDJSON stream with text deltas, one tool call, and a finish event.
	body := commandCodeNDJSON(ccEvText1, ccEvText2, ccEvToolCall, ccEvFinishTools)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then text deltas accumulate to the full text and exactly one chunk
	// carries the tool call with its upstream ID and JSON arguments.
	if errs := commandCodeErrChunks(chunks); len(errs) != 0 {
		t.Fatalf("stream emitted %d error chunks, want none: %v", len(errs), errs)
	}
	if got := commandCodeStreamedText(chunks); got != "Hello world" {
		t.Fatalf("streamed text = %q, want %q", got, "Hello world")
	}
	var toolCall gjson.Result
	for _, chunk := range chunks {
		if chunk.Err != nil || len(chunk.Payload) == 0 {
			continue
		}
		calls := gjson.GetBytes(chunk.Payload, "choices.0.delta.tool_calls")
		if calls.IsArray() && len(calls.Array()) > 0 {
			toolCall = calls.Array()[0]
		}
	}
	if !toolCall.Exists() {
		t.Fatalf("no stream chunk carried delta.tool_calls")
	}
	if got := toolCall.Get("id").String(); got != "call_abc123" {
		t.Fatalf("delta.tool_calls[0].id = %q, want %q", got, "call_abc123")
	}
	if got := toolCall.Get("function.name").String(); got != "vision_lookup" {
		t.Fatalf("delta.tool_calls[0].function.name = %q, want %q", got, "vision_lookup")
	}
	arguments := toolCall.Get("function.arguments").String()
	if got := gjson.GetBytes([]byte(arguments), "query").String(); got != "sparrow" {
		t.Fatalf("delta.tool_calls[0].function.arguments query = %q, want %q (raw=%s)", got, "sparrow", arguments)
	}
}

func Test_CommandCodeStream_ExecuteStream_normalizes_finish_reason_tool_calls(t *testing.T) {
	// Given a finish event whose raw finishReason is the npm "tool-calls".
	body := commandCodeNDJSON(ccEvText1, ccEvToolCall, ccEvFinishTools)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the success finish chunk uses the npm-normalized tool_calls reason.
	finishes := commandCodeFinishChunks(chunks)
	if len(finishes) != 1 {
		t.Fatalf("stream emitted %d finish chunks, want exactly 1", len(finishes))
	}
	if got := finishes[0].Get("choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want npm-normalized %q", got, "tool_calls")
	}
}

func Test_CommandCodeStream_ExecuteStream_normalizes_finish_reason_length_to_max_tokens(t *testing.T) {
	// Given a finish event whose raw finishReason is the npm "length".
	body := commandCodeNDJSON(ccEvText1, ccEvFinishLength)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the success finish chunk uses the npm-normalized max_tokens reason.
	finishes := commandCodeFinishChunks(chunks)
	if len(finishes) != 1 {
		t.Fatalf("stream emitted %d finish chunks, want exactly 1", len(finishes))
	}
	if got := finishes[0].Get("choices.0.finish_reason").String(); got != "max_tokens" {
		t.Fatalf("finish_reason = %q, want npm-normalized %q", got, "max_tokens")
	}
}

func Test_CommandCodeStream_ExecuteStream_tool_result_output_is_surfaced(t *testing.T) {
	// Given a provider-executed tool-result event between tool call and finish.
	body := commandCodeNDJSON(ccEvText1, ccEvToolCall, ccEvToolResult, ccEvFinishTools)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the tool result output text streams to the client instead of
	// being silently dropped.
	if errs := commandCodeErrChunks(chunks); len(errs) != 0 {
		t.Fatalf("stream emitted %d error chunks, want none: %v", len(errs), errs)
	}
	streamed := commandCodeStreamedText(chunks)
	if !strings.Contains(streamed, "found 3 results for sparrow") {
		t.Fatalf("streamed text = %q, want it to surface the tool-result output %q", streamed, "found 3 results for sparrow")
	}
}

func Test_CommandCodeStream_ExecuteStream_exposes_reasoning_content(t *testing.T) {
	// Given reasoning events wrapping the visible text.
	body := commandCodeNDJSON(ccEvReasoningOpen, ccEvReasoningText, ccEvReasoningEnd, ccEvText1, ccEvFinishStop)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the reasoning text is exposed as reasoning_content in a chunk.
	found := false
	for _, chunk := range chunks {
		if chunk.Err != nil {
			continue
		}
		if strings.Contains(string(chunk.Payload), "secret chain of thought") {
			found = true
		}
	}
	if !found {
		t.Fatal("no chunk exposes the reasoning text as reasoning_content")
	}
	if got := commandCodeStreamedText(chunks); got != "Hello " {
		t.Fatalf("streamed text = %q, want only the visible text %q", got, "Hello ")
	}
}

func Test_CommandCodeStream_ExecuteStream_parses_cache_usage_in_finish_chunk(t *testing.T) {
	// Given a finish event carrying cache token details and system prompt tokens.
	body := commandCodeNDJSON(ccEvText1, ccEvFinishRich)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the usage emitted on the stream keeps exact totals and parses the
	// cache read/write tokens into OpenAI-compatible usage details.
	usage := commandCodeUsagePayload(t, chunks)
	if got := usage.Get("prompt_tokens").Int(); got != 1200 {
		t.Fatalf("usage.prompt_tokens = %d, want %d", got, 1200)
	}
	if got := usage.Get("completion_tokens").Int(); got != 300 {
		t.Fatalf("usage.completion_tokens = %d, want %d", got, 300)
	}
	if got := usage.Get("total_tokens").Int(); got != 1500 {
		t.Fatalf("usage.total_tokens = %d, want %d", got, 1500)
	}
	if got := usage.Get("prompt_tokens_details.cached_tokens").Int(); got != 800 {
		t.Fatalf("usage.prompt_tokens_details.cached_tokens = %d, want cacheReadTokens %d", got, 800)
	}
	if got := usage.Get("prompt_tokens_details.cached_creation_tokens").Int(); got != 150 {
		t.Fatalf("usage.prompt_tokens_details.cached_creation_tokens = %d, want cacheWriteTokens %d", got, 150)
	}
}

func Test_CommandCodeStream_ExecuteStream_error_event_emits_single_error_chunk(t *testing.T) {
	// Given an upstream error event followed by a finish line; npm throws on
	// the error event and never reaches the finish.
	body := commandCodeNDJSON(ccEvText1, ccEvError429, ccEvFinishStop)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the stream emits exactly one error chunk (typed 429 with the
	// upstream message) as its terminal chunk, and no success finish chunk
	// (and therefore no [DONE]) is emitted after the error.
	errs := commandCodeErrChunks(chunks)
	if len(errs) != 1 {
		t.Fatalf("stream emitted %d error chunks, want exactly 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "quota exceeded") {
		t.Fatalf("stream error = %q, want it to carry the upstream message %q", errs[0].Error(), "quota exceeded")
	}
	requireCommandCodeStatusError(t, errs[0], http.StatusTooManyRequests)
	if finishes := commandCodeFinishChunks(chunks); len(finishes) != 0 {
		t.Fatalf("stream emitted %d success finish chunks after an error event, want none (no [DONE])", len(finishes))
	}
	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Fatalf("last stream chunk = %s, want the terminal error chunk", last.Payload)
	}
}

func Test_CommandCodeStream_ExecuteStream_eof_without_terminal_is_truncation(t *testing.T) {
	// Given a stream that ends after text without any finish or abort event.
	body := commandCodeNDJSON(ccEvText1, ccEvText2)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the stream emits exactly one typed 502 truncation error chunk and
	// no success finish chunk (and therefore no [DONE]).
	errs := commandCodeErrChunks(chunks)
	if len(errs) != 1 {
		t.Fatalf("stream emitted %d error chunks, want exactly 1 truncation error: %v", len(errs), errs)
	}
	if !strings.Contains(strings.ToLower(errs[0].Error()), "truncat") {
		t.Fatalf("stream error = %q, want a truncation message", errs[0].Error())
	}
	requireCommandCodeStatusError(t, errs[0], http.StatusBadGateway)
	if finishes := commandCodeFinishChunks(chunks); len(finishes) != 0 {
		t.Fatalf("stream emitted %d success finish chunks on a truncated stream, want none (no [DONE])", len(finishes))
	}
}

func Test_CommandCodeStream_ExecuteStream_skips_malformed_ndjson_lines(t *testing.T) {
	// Given an NDJSON stream with malformed lines interleaved between valid
	// events.
	body := commandCodeNDJSON(ccEvText1, `{not-json-at-all`, ccEvText2, `{"type":"finish",`, ccEvFinishStop)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the malformed lines are skipped: no error chunk, the full text
	// streams through, and exactly one success finish chunk terminates.
	if errs := commandCodeErrChunks(chunks); len(errs) != 0 {
		t.Fatalf("stream emitted %d error chunks, want malformed NDJSON lines skipped: %v", len(errs), errs)
	}
	if got := commandCodeStreamedText(chunks); got != "Hello world" {
		t.Fatalf("streamed text = %q, want %q", got, "Hello world")
	}
	if finishes := commandCodeFinishChunks(chunks); len(finishes) != 1 {
		t.Fatalf("stream emitted %d finish chunks, want exactly 1", len(finishes))
	}
}

func Test_CommandCodeStream_ExecuteStream_string_error_event_emits_single_error_chunk(t *testing.T) {
	// Given an error event whose payload is a quoted string.
	body := commandCodeNDJSON(ccEvText1, ccEvErrorString, ccEvFinishStop)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the stream emits exactly one error chunk (typed 500 with the
	// upstream string message) as its terminal chunk, and no success finish
	// chunk (and therefore no [DONE]).
	errs := commandCodeErrChunks(chunks)
	if len(errs) != 1 {
		t.Fatalf("stream emitted %d error chunks, want exactly 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "provider exploded mid-stream") {
		t.Fatalf("stream error = %q, want it to carry the upstream message %q", errs[0].Error(), "provider exploded mid-stream")
	}
	requireCommandCodeStatusError(t, errs[0], http.StatusInternalServerError)
	if finishes := commandCodeFinishChunks(chunks); len(finishes) != 0 {
		t.Fatalf("stream emitted %d success finish chunks after a string error event, want none (no [DONE])", len(finishes))
	}
}

func Test_CommandCodeStream_ExecuteStream_abort_is_normal_terminal(t *testing.T) {
	// Given a stream that aborts after partial text; npm 1.6.0 treats abort
	// as a normal (non-error) terminal event and keeps the accumulated content.
	body := commandCodeNDJSON(ccEvText1, ccEvAbort)

	// When
	chunks := streamCommandCodeNDJSON(t, body)

	// Then the stream completes gracefully: no error chunk, the partial text
	// is preserved, and exactly one success finish chunk terminates the
	// stream (the observable proxy for the terminating [DONE]).
	if errs := commandCodeErrChunks(chunks); len(errs) != 0 {
		t.Fatalf("stream emitted %d error chunks on abort, want none (abort is a normal terminal): %v", len(errs), errs)
	}
	if got := commandCodeStreamedText(chunks); got != "Hello " {
		t.Fatalf("streamed text = %q, want the accumulated partial text %q", got, "Hello ")
	}
	if finishes := commandCodeFinishChunks(chunks); len(finishes) != 1 {
		t.Fatalf("stream emitted %d finish chunks on abort, want exactly 1 graceful terminal chunk", len(finishes))
	}
}

// --- Raw retention bounds ---

func Test_CommandCodeStream_rawRetention_stays_bounded_and_releases(t *testing.T) {
	// Given a large stream of unknown events (no known event ever arrives):
	// raw retention exists so a legacy envelope body can still be recovered,
	// but it must stay bounded.
	acc := newCommandCodeStreamAccumulator()
	filler := []byte(`{"type":"mystery-event","data":"` + strings.Repeat("x", 64*1024) + `"}`)
	iterations := 4 * commandCodeMaxRetainedRawBytes / len(filler)
	for i := 0; i < iterations; i++ {
		acc.feedLine(filler)
	}

	// Then retention holds enough bytes for legacy recovery/truncation
	// analysis but never exceeds the cap, even though the stream carried 4x
	// the cap in raw bytes.
	retained := len(acc.retainedRawBody())
	if retained == 0 {
		t.Fatalf("retained raw body is empty, want enough bytes retained for legacy envelope recovery")
	}
	if retained > commandCodeMaxRetainedRawBytes {
		t.Fatalf("retained raw body = %d bytes after %d x %d-byte lines, want <= cap %d", retained, iterations, len(filler), commandCodeMaxRetainedRawBytes)
	}

	// When a known event finally arrives, retention is released (clear/swap)
	// because the body can no longer be a legacy envelope.
	acc.feedLine([]byte(ccEvText1))
	if got := len(acc.retainedRawBody()); got != 0 {
		t.Fatalf("retained raw body = %d bytes after a known event, want released (0)", got)
	}

	// And it stays released for the rest of the stream: memory stays flat on
	// very large streams.
	for i := 0; i < iterations; i++ {
		acc.feedLine(filler)
	}
	if got := len(acc.retainedRawBody()); got != 0 {
		t.Fatalf("retained raw body regrew to %d bytes after release, want flat 0", got)
	}
}

// --- Non-2xx HTTP status surfaces as a stream error chunk ---

// commandCodeStreamTestContextStatus injects a RoundTripper that answers the
// CommandCode generate request with the given HTTP status and body.
func commandCodeStreamTestContextStatus(status int, body string) context.Context {
	return context.WithValue(context.Background(), "cliproxy.roundtripper", commandCodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}))
}

func Test_CommandCodeStream_ExecuteStream_non_2xx_emits_single_error_chunk(t *testing.T) {
	// Given a non-2xx HTTP response (e.g. 429 with a quota body).
	ctx := commandCodeStreamTestContextStatus(http.StatusTooManyRequests, `{"error":"quota exceeded"}`)
	exec := NewCommandCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "user_test"}}
	payload := []byte(`{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"hello"}]}`)
	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-pro",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range result.Chunks {
		chunks = append(chunks, chunk)
	}
	errs := commandCodeErrChunks(chunks)
	if len(errs) != 1 {
		t.Fatalf("stream emitted %d error chunks, want exactly 1: %v", len(errs), errs)
	}
	requireCommandCodeStatusError(t, errs[0], http.StatusTooManyRequests)
	if !strings.Contains(errs[0].Error(), "quota exceeded") {
		t.Fatalf("stream error = %q, want the upstream body message", errs[0].Error())
	}
}

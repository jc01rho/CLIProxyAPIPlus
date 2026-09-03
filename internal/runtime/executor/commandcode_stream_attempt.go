// commandcode_stream_attempt.go holds the single-attempt NDJSON scan + OpenAI
// chunk emission helper used by CommandCodeExecutor.ExecuteStream. The pause_turn
// continuation loop in commandcode_executor.go re-invokes the helper up to
// commandCodeMaxContinuations additional times, mirroring the installed
// command-code@1.12.0 client createModelClient.complete behavior.

package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// commandCodeAttemptContext bundles the immutable translation/state for one
// stream attempt so the helper does not repeat work across pause_turn posts.
type commandCodeAttemptContext struct {
	ctx            context.Context
	cfg            *config.Config
	auth           *cliproxyauth.Auth
	provider       string
	baseModel      string
	from           sdktranslator.Format
	to             sdktranslator.Format
	wirePayload    []byte
	openAIInput    []byte
	original       []byte
	out            chan cliproxyexecutor.StreamChunk
	reporter       *usageReporter
	chatID         string
	toolCallOffset int
	sessionID      string
}

// commandCodeStreamOneAttempt posts one /alpha/generate request and scans its
// NDJSON into OpenAI-shape stream chunks. The returned accumulator is
// terminated; the caller decides whether to continue based on IsPauseTurn().
// On a terminal stream error it pushes exactly one error chunk and returns the
// accumulator; on scanner/EOF failures it also pushes a single error chunk.
func commandCodeStreamOneAttempt(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	provider, baseModel, apiKey string,
	attemptCtx commandCodeAttemptContext,
) (*commandCodeStreamAccumulator, error) {
	url := commandCodeGenerateURL(auth)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(attemptCtx.wirePayload))
	if err != nil {
		return nil, err
	}
	applyCommandCodeHeaders(httpReq, apiKey, attemptCtx.sessionID)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      attemptCtx.wirePayload,
		Provider:  provider,
		AuthID:    authID,
		Model:     baseModel,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, cfg, err)
		return nil, err
	}

	recordAPIResponseMetadata(ctx, cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, cfg, b)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("commandcode executor: close response body error: %v", errClose)
		}
		// A 400/422 reasoning-effort rejection may mean the static ladder is
		// stale: refresh the model's profile and, when the refreshed ladder no
		// longer supports the requested effort, retry once without the field.
		if retryPayload := commandCodeEffortRejectionRetryPayload(ctx, cfg, auth, attemptCtx.wirePayload, httpResp.StatusCode, string(b)); retryPayload != nil {
			retryCtx := attemptCtx
			retryCtx.wirePayload = retryPayload
			return commandCodeStreamOneAttempt(ctx, cfg, auth, provider, baseModel, apiKey, retryCtx)
		}
		streamErr := statusErr{code: httpResp.StatusCode, msg: string(b)}
		recordAPIResponseError(ctx, cfg, streamErr)
		attemptCtx.reporter.publishFailure(ctx)
		select {
		case attemptCtx.out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
		case <-ctx.Done():
		}
		return nil, streamErr
	}

	bodyErr := func(err error) {
		recordAPIResponseError(ctx, cfg, err)
		attemptCtx.reporter.publishFailure(ctx)
		select {
		case attemptCtx.out <- cliproxyexecutor.StreamChunk{Err: err}:
		case <-ctx.Done():
		}
	}

	acc := newCommandCodeStreamAccumulator()
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800)
	var param any
	toolCallIndex := attemptCtx.toolCallOffset

	sendSSELine := func(sseLine []byte) {
		appendAPIResponseChunk(ctx, cfg, sseLine)
		if detail, ok := parseOpenAIStreamUsage(sseLine); ok {
			attemptCtx.reporter.publish(ctx, detail)
		}
		chunks := sdktranslator.TranslateStream(ctx, attemptCtx.to, attemptCtx.from, attemptCtx.baseModel, attemptCtx.original, attemptCtx.openAIInput, bytes.Clone(sseLine), &param)
		for i := range chunks {
			select {
			case attemptCtx.out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
			case <-ctx.Done():
				return
			}
		}
	}
	emitChunk := func(payload map[string]any) {
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return
		}
		sendSSELine(append([]byte("data: "), b...))
	}
	deltaFor := func(delta map[string]any) {
		emitChunk(commandCodeStreamChunk(attemptCtx.chatID, baseModel, delta, ""))
	}

	for scanner.Scan() {
		trimmed := bytes.TrimSpace(scanner.Bytes())
		if len(trimmed) == 0 {
			continue
		}
		event := decodeCommandCodeStreamEvent(trimmed)
		acc.feed(event)
		switch event.kind {
		case commandCodeEventTextDelta, commandCodeEventToolResult:
			if event.text != "" {
				deltaFor(map[string]any{"role": "assistant", "content": event.text})
			}
		case commandCodeEventReasoningDelta:
			// Expose hidden reasoning to the client as reasoning_content, the
			// OpenAI-compatible stream equivalent of opencodex's thinking_delta.
			if event.text != "" {
				deltaFor(map[string]any{"role": "assistant", "reasoning_content": event.text})
			}
		case commandCodeEventToolCall:
			deltaFor(map[string]any{
				"tool_calls": []map[string]any{{
					"index": toolCallIndex,
					"id":    event.toolCallID,
					"type":  "function",
					"function": map[string]any{
						"name":      event.toolName,
						"arguments": string(event.toolInput),
					},
				}},
			})
			toolCallIndex++
		case commandCodeEventFinish, commandCodeEventAbort:
			emitChunk(map[string]any{
				"id":      attemptCtx.chatID,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   baseModel,
				"choices": []map[string]any{{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": acc.stopReason,
				}},
				"usage": commandCodeOpenAIUsage(acc.usage()),
			})
			if !acc.IsPauseTurn() {
				sendSSELine([]byte("data: [DONE]"))
			}
		}
		if acc.terminal != commandCodeTerminalNone {
			break
		}
	}
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("commandcode executor: close response body error: %v", errClose)
	}
	if errScan := scanner.Err(); errScan != nil {
		bodyErr(errScan)
		return acc, errScan
	}
	streamErr := acc.err
	if streamErr == nil {
		// finishEOF only applies when no terminal event was seen; on a pause_turn
		// finish the chain is not truncated, so do not classify pause_turn EOF
		// itself as truncation here — the continuation caller handles that.
		if acc.terminal == commandCodeTerminalNone && !acc.IsPauseTurn() {
			streamErr = acc.finishEOF()
		}
	}
	if streamErr != nil {
		bodyErr(streamErr)
	}
	return acc, streamErr
}

// resetToContinuationStart adjusts a streaming accumulator so the next attempt
// only emits newly observed deltas; the master accumulator carries the merged
// history (content/usage/tool indices) while per-attempt chunks resume fresh.
func (a *commandCodeStreamAccumulator) continuationSnapshot() commandCodeStreamAccumulator {
	return commandCodeStreamAccumulator{
		text:        a.text,
		toolCalls:   a.toolCalls,
		final:       a.final,
		hasFinal:    a.hasFinal,
		incremental: a.incremental,
		stopReason:  "stop",
	}
}

var _ = strings.TrimSpace

// commandcode_continuation.go holds the shared HTTP attempt and pause_turn
// continuation orchestration used by both Execute and ExecuteStream. The
// installed command-code@1.12.0 client (createModelClient.complete) re-posts
// the same request up to commandCodeMaxContinuations additional times when the
// upstream stream finishes with the raw reason pause_turn, accumulating the
// visible content and usage across all completion sessions into one logical
// turn; the proxy mirrors that so a long generation is not silently truncated.

package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// commandCodeSessionIDLength is the hex-digit count of the random suffix of the
// x-session-id header, matching the createModelClient format (sess_<16hex>)
// emitted by the installed command-code@1.12.0 client. The header is required
// by the upstream API to associate pause_turn continuation posts with the
// original session.
const commandCodeSessionIDLength = 16

// newCommandCodeSessionID generates a fresh sess_<16hex> session identifier
// used as the x-session-id header value. One identifier is generated per
// request and reused across all pause_turn continuation posts of that request,
// mirroring the per-session value held by the real client.
func newCommandCodeSessionID() string {
	buf := make([]byte, commandCodeSessionIDLength/2)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand should never fail in normal operation; fall back to a
		// non-secret placeholder rather than aborting the request.
		return "sess_0000000000000000"
	}
	return "sess_" + hex.EncodeToString(buf)
}

// commandCodeAttemptResult is the outcome of a single /alpha/generate POST and
// NDJSON scan: the parsed accumulator and the raw body lines observed (kept
// only for legacy envelope recovery on the first session).
type commandCodeAttemptResult struct {
	acc      *commandCodeStreamAccumulator
	rawLines [][]byte
}

// commandCodePostOne sends one /alpha/generate request and scans its NDJSON
// body into a fresh accumulator. It returns the accumulator (terminated by a
// finish/abort/error event or by EOF) and the observed raw lines. The caller
// owns response-body closure on every path except the successful 2xx path,
// where this helper scans to completion (or terminal) and closes the body.
func commandCodePostOne(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	provider string,
	payload []byte,
	apiKey string,
	sessionID string,
	continuation int,
) (out commandCodeAttemptResult, err error) {
	url := commandCodeGenerateURL(auth)
	httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if reqErr != nil {
		return out, reqErr
	}
	applyCommandCodeHeaders(httpReq, apiKey, sessionID)
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
		Body:      payload,
		Provider:  provider,
		AuthID:    authID,
		Model:     gjsonModel(payload),
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, cfg, err)
		return out, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("commandcode executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, cfg, b)
		// A 400/422 reasoning-effort rejection may mean the static ladder is
		// stale: refresh the model's profile and, when the refreshed ladder no
		// longer supports the requested effort, retry once without the field.
		if retryPayload := commandCodeEffortRejectionRetryPayload(ctx, cfg, auth, payload, httpResp.StatusCode, string(b)); retryPayload != nil {
			return commandCodePostOne(ctx, cfg, auth, provider, retryPayload, apiKey, sessionID, continuation)
		}
		return out, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	acc := newCommandCodeStreamAccumulator()
	scanner := newCommandCodeBodyScanner(httpResp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		appendAPIResponseChunk(ctx, cfg, line)
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		out.rawLines = append(out.rawLines, append([]byte(nil), trimmed...))
		acc.feed(decodeCommandCodeStreamEvent(trimmed))
		if acc.terminal != commandCodeTerminalNone {
			break
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		recordAPIResponseError(ctx, cfg, errScan)
		return out, errScan
	}
	out.acc = acc
	return out, nil
}

// commandCodePauseChain runs the full pause_turn continuation chain for the
// non-stream Execute path: it posts the request, folds the first session into
// the returned accumulator, and while that session terminated on a pause_turn
// finish reason it re-posts up to commandCodeMaxContinuations more times,
// appending each session's visible text/tool calls and accumulating usage.
// A terminal error on any session short-circuits and is returned.
func commandCodePauseChain(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	provider string,
	payload []byte,
	apiKey string,
) (*commandCodeStreamAccumulator, [][]byte, error) {
	var rawLines [][]byte
	master := newCommandCodeStreamAccumulator()
	sessionID := newCommandCodeSessionID()
	for attempt := 0; ; attempt++ {
		result, err := commandCodePostOne(ctx, cfg, auth, provider, payload, apiKey, sessionID, attempt)
		if err != nil {
			return nil, nil, err
		}
		acc := result.acc
		if acc.err != nil {
			recordAPIResponseError(ctx, cfg, acc.err)
			return nil, nil, acc.err
		}
		if attempt == 0 {
			rawLines = result.rawLines
		}
		master.merge(acc)

		if !acc.IsPauseTurn() || attempt >= commandCodeMaxContinuations {
			// Legacy envelope recovery only applies when no known event was
			// ever seen across the whole chain; check after merging.
			if attempt == 0 && !master.sawKnownEvent {
				if fallbackText := commandCodeRecoverFromRawBody(rawLines); fallbackText != "" {
					master.adoptLegacyText(fallbackText)
				}
			}
			if errEOF := master.finishEOF(); errEOF != nil {
				recordAPIResponseError(ctx, cfg, errEOF)
				return nil, nil, errEOF
			}
			return master, rawLines, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
}

// gjsonModel reads the model field from a wire params payload for request
// logging. It tolerates a missing field (the model may be passed out-of-band).
func gjsonModel(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var probe struct {
		Params struct {
			Model string `json:"model"`
		} `json:"params"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return ""
	}
	if probe.Params.Model != "" {
		return probe.Params.Model
	}
	return probe.Model
}

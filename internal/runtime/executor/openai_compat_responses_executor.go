package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *OpenAICompatExecutor) useNativeResponses(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	compat := e.resolveCompatConfig(auth, req)
	if compat == nil {
		return false
	}
	return helps.CompatModelResponsesOnly(compat, thinking.ParseSuffix(req.Model).ModelName, thinking.ParseSuffix(helps.PayloadRequestedModel(opts, req.Model)).ModelName, looksLikeOpencodeZenBaseURL(compat.BaseURL))
}

// openNativeResponses shares request preparation and transport for both modes,
// without running chat sanitizers or Codex native request normalization.
// When relaxToolChoice is set, a forced tool_choice is downgraded to "auto".
// The translated body is returned even on upstream errors so callers can
// inspect the tool_choice that was actually sent.
func (e *OpenAICompatExecutor) openNativeResponses(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool, reporter *helps.UsageReporter, relaxToolChoice bool) (*http.Response, []byte, error) {
	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		return nil, nil, statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	cfg := e.cfg.ForAPIKey()
	original := opts.OriginalRequest
	if len(original) == 0 {
		original = req.Payload
	}
	baseline, body, changed := helps.TranslateCompatResponsesRequest(ctx, opts.Headers, cfg, opts.SourceFormat, baseModel, original, req.Payload, stream, helps.APIKeyModelIsCompat(req))
	var err error
	if opts.SourceFormat != sdktranslator.FormatOpenAIResponse || thinking.ParseSuffix(req.Model).HasSuffix || changed {
		body, err = helps.ApplyRequestThinkingWithContext(ctx, body, req, opts, opts.SourceFormat.String(), "openai-response", e.Identifier(), changed)
		if err != nil {
			return nil, nil, err
		}
	}
	body = helps.ApplyPayloadConfigWithRequest(cfg, baseModel, "openai-response", opts.SourceFormat.String(), "", body, baseline, helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers)
	body, err = e.applyPromptCacheKey(ctx, auth, opts.SourceFormat, baseModel, req, opts, body)
	if err != nil {
		return nil, nil, err
	}
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = helps.SetBoolIfDifferent(body, "stream", stream)
	if relaxToolChoice && nativeResponsesForcedToolChoice(body) {
		body, _ = sjson.SetBytes(body, "tool_choice", "auto")
	}
	reporter.SetTranslatedReasoningEffort(body, "openai-response")
	url := strings.TrimRight(baseURL, "/") + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
	}
	var attrs map[string]string
	var authID, authLabel, authType, authValue string
	if auth != nil {
		attrs = auth.Attributes
		authID, authLabel = auth.ID, auth.Label
		authType, authValue = auth.AccountInfo()
	}
	applyOpencodeZenFingerprint(httpReq, e.provider, body, opts.Headers, apiKey)
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: body, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0))
	httpResp, err := client.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, cfg, err)
		return nil, nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		defer closeCompatResponsesBody(httpResp.Body)
		data, errRead := io.ReadAll(httpResp.Body)
		if errRead != nil {
			return nil, nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, cfg, data)
		return nil, body, newNativeResponsesError(httpResp.StatusCode, httpResp.Header, data)
	}
	return httpResp, body, nil
}

func (e *OpenAICompatExecutor) executeNativeResponses(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ cliproxyexecutor.Response, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, thinking.ParseSuffix(req.Model).ModelName, auth)
	defer reporter.TrackFailure(ctx, &err)
	var (
		httpResp *http.Response
		body     []byte
		data     []byte
		status   string
	)
	for relax := false; ; relax = true {
		httpResp, body, err = e.openNativeResponses(ctx, auth, req, opts, false, reporter, relax)
		if err != nil {
			if !relax && nativeResponsesForcedToolChoiceRejected(body, err) {
				logForcedToolChoiceRelax(req.Model, err)
				continue
			}
			return cliproxyexecutor.Response{}, err
		}
		data, err = io.ReadAll(httpResp.Body)
		closeCompatResponsesBody(httpResp.Body)
		if err != nil {
			return cliproxyexecutor.Response{}, err
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		status = gjson.GetBytes(data, "status").String()
		if !gjson.ValidBytes(data) || openAICompatHasStructuredError(data) || status == "failed" || status == "cancelled" {
			err = newNativeResponsesError(httpResp.StatusCode, httpResp.Header, data)
			if !relax && nativeResponsesForcedToolChoiceRejected(body, err) {
				logForcedToolChoiceRelax(req.Model, err)
				continue
			}
			reporter.ObserveResponseModel(data)
			reporter.PublishFailureWithDetail(ctx, helps.ParseOpenAIUsage(data), err)
			return cliproxyexecutor.Response{}, err
		}
		break
	}
	reporter.ObserveResponseModel(data)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	reporter.EnsurePublished(ctx)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := helps.CompatResponsesTranslationFormat(responseFormat)
	if to == sdktranslator.FormatCodex {
		event := "response.completed"
		if status == "incomplete" {
			event = "response.incomplete"
		}
		data = append([]byte(`{"type":"`+event+`","response":`), append(data, '}')...)
	}
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, data, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

// frameEntry preserves upstream order for replay after buffering.
type frameEntry struct {
	event string
	data  []byte
}

func (e *OpenAICompatExecutor) executeNativeResponsesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, thinking.ParseSuffix(req.Model).ModelName, auth)
	defer reporter.TrackFailure(ctx, &err)
	relaxed := false
	httpResp, body, err := e.openNativeResponses(ctx, auth, req, opts, true, reporter, false)
	if err != nil && nativeResponsesForcedToolChoiceRejected(body, err) {
		logForcedToolChoiceRelax(req.Model, err)
		relaxed = true
		httpResp, body, err = e.openNativeResponses(ctx, auth, req, opts, true, reporter, true)
	}
	if err != nil {
		return nil, err
	}
	headers := httpResp.Header.Clone()
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() { closeCompatResponsesBody(httpResp.Body) }()
		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		var usage helps.StreamUsageBuffer
		fail := func(err error) {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			usage.PublishFailure(ctx, reporter, err)
			send(cliproxyexecutor.StreamChunk{Err: err})
		}
		reader := helps.NewResponsesEventReader(httpResp.Body)
		responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
		to := helps.CompatResponsesTranslationFormat(responseFormat)
		inputTokens := helps.NewClaudeInputTokenState(opts.SourceFormat, to, responseFormat, opts.OriginalRequest)
		var param any

		// Bootstrap buffer: hold lifecycle frames until first meaningful
		// output (text/reasoning delta, or a function/custom tool call with
		// id+name), then flush in original order. Bounded by a byte cap so
		// memory cannot grow unbounded. Upstreams echo the full request
		// (instructions, tools) in response.created/in_progress, so the cap must
		// comfortably hold those echoes: a failure before any output must stay
		// pre-stream so conductor retry/fallback can still handle it. Hitting
		// the cap releases the stream (flush and pass through) instead of
		// failing it. Failures after release are delivered in-stream, not retried.
		const bootstrapByteCap = 8 << 20
		var buffered []frameEntry
		bufferedBytes := 0
		var meaningful bool
		emit := func(event string, data []byte) bool {
			compact := new(bytes.Buffer)
			if errCompact := json.Compact(compact, data); errCompact != nil {
				fail(errCompact)
				return false
			}
			frame := append([]byte("data: "), compact.Bytes()...)
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				frame = append([]byte("event: "+event+"\n"), frame...)
				frame = append(frame, '\n', '\n')
			}
			for _, chunk := range helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, frame, &param, inputTokens) {
				if !send(cliproxyexecutor.StreamChunk{Payload: chunk}) {
					fail(ctx.Err())
					return false
				}
			}
			return true
		}

		for {
			event, data, errRead := reader.Next()
			if errRead != nil {
				if ctx.Err() != nil {
					errRead = ctx.Err()
				} else if errRead == io.EOF {
					errRead = fmt.Errorf("Responses stream ended without a terminal event: %w", io.ErrUnexpectedEOF)
				}
				fail(errRead)
				return
			}
			helps.AppendAPIResponseChunk(ctx, e.cfg, data)
			if !json.Valid(data) {
				fail(statusErr{code: http.StatusBadGateway, msg: "invalid Responses event JSON"})
				return
			}
			if typ := gjson.GetBytes(data, "type").String(); typ != "" {
				event = typ
			} else if event != "" {
				data, _ = sjson.SetBytes(data, "type", event)
			}
			reporter.ObserveResponseModel(data)
			helps.ObserveResponsesTokenEvent(reporter, data)
			detail, ok := helps.ParseCodexUsage(data)
			usage.Observe(detail, ok)

			isErrorEvent := event == "error" || event == "response.failed" || openAICompatHasStructuredError(data) || gjson.GetBytes(data, "response.status").String() == "failed"
			if isErrorEvent {
				errEvent := newNativeResponsesError(httpResp.StatusCode, httpResp.Header, data)
				// Nothing has been emitted yet, so a rejected forced tool_choice
				// can be retried once with "auto" transparently.
				if !meaningful && !relaxed && nativeResponsesForcedToolChoiceRejected(body, errEvent) {
					logForcedToolChoiceRelax(req.Model, errEvent)
					relaxed = true
					closeCompatResponsesBody(httpResp.Body)
					retryResp, retryBody, errRetry := e.openNativeResponses(ctx, auth, req, opts, true, reporter, true)
					if errRetry != nil {
						httpResp = &http.Response{Body: http.NoBody}
						fail(errRetry)
						return
					}
					httpResp, body = retryResp, retryBody
					reader = helps.NewResponsesEventReader(httpResp.Body)
					inputTokens = helps.NewClaudeInputTokenState(opts.SourceFormat, to, responseFormat, opts.OriginalRequest)
					param = nil
					usage = helps.StreamUsageBuffer{}
					buffered = nil
					bufferedBytes = 0
					continue
				}
				fail(errEvent)
				return
			}
			terminal := event == "response.completed" || event == "response.incomplete"

			frameBytes := len(event) + len(data) + 32
			if !meaningful && (terminal || helps.IsResponsesMeaningfulOutputEvent(event, data) || bufferedBytes+frameBytes > bootstrapByteCap) {
				meaningful = true
				for _, pending := range buffered {
					if !emit(pending.event, pending.data) {
						return
					}
				}
				buffered = nil
			}

			if !meaningful {
				copyData := append([]byte{}, data...)
				buffered = append(buffered, frameEntry{event: event, data: copyData})
				bufferedBytes += frameBytes
				continue
			}

			if !emit(event, data) {
				return
			}
			if terminal {
				usage.Publish(ctx, reporter)
				reporter.EnsurePublished(ctx)
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

// nativeResponsesForcedToolChoice reports whether tool_choice forces a tool call
// ("required" or an object naming a tool/function/allowed set).
func nativeResponsesForcedToolChoice(body []byte) bool {
	tc := gjson.GetBytes(body, "tool_choice")
	switch {
	case tc.Type == gjson.String:
		return tc.String() == "required"
	case tc.IsObject():
		typ := tc.Get("type").String()
		if typ == "allowed_tools" {
			return tc.Get("mode").String() == "required"
		}
		return typ != "" && typ != "auto" && typ != "none"
	default:
		return false
	}
}

// Some Responses upstreams (e.g. Claude behind maxrouter) reject any forced
// tool_choice with HTTP 400 while accepting the same request with "auto".
func nativeResponsesForcedToolChoiceRejected(body []byte, err error) bool {
	var nativeErr nativeResponsesError
	if !errors.As(err, &nativeErr) || nativeErr.code != http.StatusBadRequest {
		return false
	}
	return nativeResponsesForcedToolChoice(body)
}

func logForcedToolChoiceRelax(model string, err error) {
	log.WithField("model", model).Warnf("openai compat responses: forced tool_choice rejected with 400, retrying once with tool_choice=auto: %v", err)
}

func closeCompatResponsesBody(body io.Closer) {
	if err := body.Close(); err != nil {
		log.Errorf("openai compat responses: close response body: %v", err)
	}
}

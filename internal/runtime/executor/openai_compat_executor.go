package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAICompatImageHandlerType            = "openai-image"
	openAICompatImagesGenerationsPath       = "/images/generations"
	openAICompatImagesEditsPath             = "/images/edits"
	openAICompatDefaultImageEndpoint        = openAICompatImagesGenerationsPath
	openAICompatMultipartMemory       int64 = 32 << 20
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config

	developerRoleCacheMu sync.Mutex
	developerRoleCache   *openAICompatDeveloperRoleCache
	now                  func() time.Time
}

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest injects OpenAI-compatible credentials into the outgoing HTTP request.
func (e *OpenAICompatExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects OpenAI-compatible credentials into the request and executes it.
func (e *OpenAICompatExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("openai compat executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	if opts.Alt == "responses/compact" {
		to = sdktranslator.FromString("openai-response")
		endpoint = "/responses/compact"
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, opts.Stream, isCompat)
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, opts.Stream, isCompat)

	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	compatCfg := e.resolveCompatConfig(auth)
	if helps.ShouldNormalizeOpenAIToolResultsForModel(compatCfg, baseModel, requestedModel) {
		translated = helps.NormalizeOpenAIToolResultsTextOnly(translated)
	}
	// Provider-specific request transformations
	// Resolve conflicts between "reasoning" object and "reasoning_effort" string
	translated = resolveReasoningEffortConflict(translated)
	translated = normalizeMistralReasoningEffort(baseModel, translated)
	if isMiMoModel(baseModel) {
		translated = applyMiMoReasoningBackfill(translated)
	}
	// Apply reasoning normalization for all providers when reasoning signals are present
	if !isMistralProvider(e.provider) {
		if normalized, _, errNorm := normalizeOpenAIToolCallReasoningContentWithOptions(translated, openAIReasoningNormalizationOptions{
			requireReasoningSignal: true,
		}); errNorm == nil {
			translated = normalized
		}
	}
	if isMistralProvider(e.provider) {
		translated = stripMistralUnsupportedFields(translated)
	}
	// Strip unsupported top-level fields for DeepSeek models
	if isDeepSeekModel(baseModel) {
		translated = stripDeepSeekUnsupportedFields(translated)
	}
	if needsToolCallIDNormalization(baseModel, compatCfg) {
		if normalized, patched, errNorm := normalizeNVIDIAToolCallIDs(translated); patched > 0 && errNorm == nil {
			translated = normalized
		}
	}
	if isNVIDIACompatProvider(compatCfg) {
		translated = applyNVIDIAMaxTokensReduction(translated)
	}
	if isSensenovaCompatProvider(compatCfg) {
		translated = applySensenovaMaxTokensClamp(translated)
		translated = sanitizeSensenovaToolCalls(translated)
	}
	// Empty `tools[].function.name` is rejected with invalid_request_error by
	// every OpenAI-compatible API (e.g. Upstage Solar). Drop the offending
	// entries unconditionally so any openai-compat provider stays usable when
	// upstream clients send malformed tool catalogs.
	translated = sanitizeEmptyToolFunctionNames(translated)
	// Orphaned tool results (result whose tool_call_id has no matching
	// assistant tool_call) make strict upstreams such as MiniMax reject the
	// request with 400 error 2013. Drop them after all other sanitizers so
	// results orphaned by those sanitizers are covered too.
	translated = dropOrphanOpenAIToolResults(translated)
	translated = stripOpenAICompatProviderUnsupportedFields(e.provider, compatCfg, translated)
	if opts.Alt != "responses/compact" {
		translated, err = e.applyPromptCacheKey(ctx, auth, from, baseModel, req, opts, translated)
		if err != nil {
			return resp, err
		}
	}
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
		translated = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "openai compat executor", translated)
	}
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	// Ensure all tool-related id fields are JSON strings (some clients send
	// numeric or null ids which upstream providers reject).
	translated = normalizeToolResultIDsToString(translated)
	developerRoleKey := e.developerRoleCapabilityKey(auth, baseURL, baseModel, endpoint)
	if endpoint == "/chat/completions" {
		translated = e.applyKnownDeveloperRoleFallback(developerRoleKey, translated)
	}

	url := strings.TrimSuffix(baseURL, "/") + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	applyOpencodeZenFingerprint(httpReq, e.provider)
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	retriedDeveloperRole := false
	if retryPayload, retry := e.developerRoleRetryPayload(developerRoleKey, translated, body, httpResp.StatusCode); retry {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close developer-role retry response body error: %v", errClose)
		}
		translated = retryPayload
		retriedDeveloperRole = true
		retryReq := cloneOpenAICompatRequestWithBody(httpReq, translated)
		helps.LogWithRequestID(ctx).Warnf("openai compat provider %s rejected role developer; retrying with normalized leading instructions", strings.TrimPrefix(e.provider, "openai-compatible-"))
		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL: url, Method: http.MethodPost, Headers: retryReq.Header.Clone(), Body: translated,
			Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue,
		})
		httpResp, err = httpClient.Do(retryReq)
		if err != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			return resp, err
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		body, errRead = io.ReadAll(httpResp.Body)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return resp, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 || openAICompatHasStructuredError(body) || (retriedDeveloperRole && !openAICompatUsableSuccessBody(endpoint, body)) {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		code := httpResp.StatusCode
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 && (openAICompatHasStructuredError(body) || (retriedDeveloperRole && !openAICompatUsableSuccessBody(endpoint, body))) {
			code = http.StatusBadGateway // 502
		}
		err = statusErr{code: code, msg: string(body)}
		return resp, err
	}
	if retriedDeveloperRole {
		e.rememberDeveloperRoleFallback(developerRoleKey)
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.EnsurePublished(ctx)
	// MiniMax-M3 embeds reasoning in content tags instead of reasoning_content.
	// Extract it before reverse translation so downstream clients receive the
	// reasoning and answer separately.
	if isMiniMaxThinkingTagModel(baseModel) {
		body = normalizeMiniMaxThinkingBody(body)
	}
	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, body, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return resp, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), false)
	if errPrepare != nil {
		err = errPrepare
		return resp, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	applyOpencodeZenFingerprint(httpReq, e.provider)
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		err = errRead
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return resp, err
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	resp = cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImagesStream(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, true, isCompat)
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, true, isCompat)

	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	compatCfg := e.resolveCompatConfig(auth)
	if helps.ShouldNormalizeOpenAIToolResultsForModel(compatCfg, baseModel, requestedModel) {
		translated = helps.NormalizeOpenAIToolResultsTextOnly(translated)
	}
	// Provider-specific request transformations
	// Resolve conflicts between "reasoning" object and "reasoning_effort" string
	translated = resolveReasoningEffortConflict(translated)
	translated = normalizeMistralReasoningEffort(baseModel, translated)
	if isMiMoModel(baseModel) {
		translated = applyMiMoReasoningBackfill(translated)
	}
	// Apply reasoning normalization for all providers when reasoning signals are present
	if !isMistralProvider(e.provider) {
		if normalized, _, errNorm := normalizeOpenAIToolCallReasoningContentWithOptions(translated, openAIReasoningNormalizationOptions{
			requireReasoningSignal: true,
		}); errNorm == nil {
			translated = normalized
		}
	}
	if isMistralProvider(e.provider) {
		translated = stripMistralUnsupportedFields(translated)
	}
	// Strip unsupported top-level fields for DeepSeek models
	if isDeepSeekModel(baseModel) {
		translated = stripDeepSeekUnsupportedFields(translated)
	}
	if needsToolCallIDNormalization(baseModel, compatCfg) {
		if normalized, patched, errNorm := normalizeNVIDIAToolCallIDs(translated); patched > 0 && errNorm == nil {
			translated = normalized
		}
	}
	if isNVIDIACompatProvider(compatCfg) {
		translated = applyNVIDIAMaxTokensReduction(translated)
	}
	if isSensenovaCompatProvider(compatCfg) {
		translated = applySensenovaMaxTokensClamp(translated)
		translated = sanitizeSensenovaToolCalls(translated)
	}
	// Empty `tools[].function.name` is rejected with invalid_request_error by
	// every OpenAI-compatible API (e.g. Upstage Solar). Drop the offending
	// entries unconditionally so any openai-compat provider stays usable when
	// upstream clients send malformed tool catalogs.
	translated = sanitizeEmptyToolFunctionNames(translated)
	// Orphaned tool results make strict upstreams such as MiniMax reject the
	// request with 400 error 2013; see dropOrphanOpenAIToolResults.
	translated = dropOrphanOpenAIToolResults(translated)
	translated = stripOpenAICompatProviderUnsupportedFields(e.provider, compatCfg, translated)
	if opts.Alt != "responses/compact" {
		translated, err = e.applyPromptCacheKey(ctx, auth, from, baseModel, req, opts, translated)
		if err != nil {
			return nil, err
		}
	}

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	translated = helps.SetBoolIfDifferent(translated, "stream_options.include_usage", true)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	// Ensure all tool-related id fields are JSON strings (some clients send
	// numeric or null ids which upstream providers reject).
	translated = normalizeToolResultIDsToString(translated)
	developerRoleKey := e.developerRoleCapabilityKey(auth, baseURL, baseModel, "/chat/completions")
	translated = e.applyKnownDeveloperRoleFallback(developerRoleKey, translated)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	applyOpencodeZenFingerprint(httpReq, e.provider)
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	retriedDeveloperRole := false
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, errRead := io.ReadAll(httpResp.Body)
		if errRead != nil {
			_ = httpResp.Body.Close()
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		if retryPayload, retry := e.developerRoleRetryPayload(developerRoleKey, translated, body, httpResp.StatusCode); retry {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close developer-role retry response body error: %v", errClose)
			}
			translated = retryPayload
			retriedDeveloperRole = true
			retryReq := cloneOpenAICompatRequestWithBody(httpReq, translated)
			helps.LogWithRequestID(ctx).Warnf("openai compat provider %s rejected role developer; retrying stream with normalized leading instructions", strings.TrimPrefix(e.provider, "openai-compatible-"))
			helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
				URL: url, Method: http.MethodPost, Headers: retryReq.Header.Clone(), Body: translated,
				Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue,
			})
			httpResp, err = httpClient.Do(retryReq)
			if err != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, err)
				return nil, err
			}
			helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		} else {
			_ = httpResp.Body.Close()
			err = statusErr{code: httpResp.StatusCode, msg: string(body)}
			return nil, err
		}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, errRead := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return nil, err
	}

	newScanner := func(body io.Reader) *bufio.Scanner {
		next := bufio.NewScanner(body)
		next.Buffer(nil, 52_428_800) // 50MB
		return next
	}
	currentBody := httpResp.Body
	scanner := newScanner(currentBody)
	var bootstrapLines [][]byte
	var bootstrapErr error
	bootstrapControls := 0
	for {
		if !scanner.Scan() {
			if errScan := scanner.Err(); errScan != nil {
				bootstrapErr = errScan
			}
			break
		}
		line := bytes.Clone(scanner.Bytes())
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		trimmedLine := bytes.TrimSpace(line)
		if len(trimmedLine) == 0 || bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("event:")) ||
			bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
			bootstrapControls++
			if bootstrapControls >= 32 {
				break
			}
			continue
		}
		data := trimmedLine
		isSSEData := bytes.HasPrefix(trimmedLine, []byte("data:"))
		if isSSEData {
			data = bytes.TrimSpace(trimmedLine[len("data:"):])
		}
		if openAICompatHasStructuredError(data) {
			if !retriedDeveloperRole && openAICompatRejectsDeveloperRole(http.StatusOK, data) {
				retryPayload, retry := e.developerRoleRetryPayload(developerRoleKey, translated, data, http.StatusOK)
				if retry {
					if errClose := currentBody.Close(); errClose != nil {
						log.Errorf("openai compat executor: close embedded developer-role retry response body error: %v", errClose)
					}
					translated = retryPayload
					retriedDeveloperRole = true
					retryReq := cloneOpenAICompatRequestWithBody(httpReq, translated)
					helps.LogWithRequestID(ctx).Warnf("openai compat provider %s returned a streamed developer-role error; retrying with normalized leading instructions", strings.TrimPrefix(e.provider, "openai-compatible-"))
					helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
						URL: url, Method: http.MethodPost, Headers: retryReq.Header.Clone(), Body: translated,
						Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue,
					})
					httpResp, err = httpClient.Do(retryReq)
					if err != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, err)
						return nil, err
					}
					helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
					currentBody = httpResp.Body
					if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
						body, errRead := io.ReadAll(currentBody)
						_ = currentBody.Close()
						if errRead != nil {
							return nil, errRead
						}
						helps.AppendAPIResponseChunk(ctx, e.cfg, body)
						err = statusErr{code: httpResp.StatusCode, msg: string(body)}
						return nil, err
					}
					scanner = newScanner(currentBody)
					continue
				}
			}
			bootstrapErr = statusErr{code: http.StatusBadGateway, msg: string(data)}
			break
		}
		if !isSSEData {
			if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
				bootstrapErr = statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}
				break
			}
			continue
		}
		bootstrapLines = append(bootstrapLines, line)
		break
	}

	returnedHeaders := httpResp.Header.Clone()
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if currentBody != nil {
				if errClose := currentBody.Close(); errClose != nil {
					log.Errorf("openai compat executor: close response body error: %v", errClose)
				}
			}
		}()
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		var streamUsage helps.StreamUsageBuffer
		var seenDone bool
		var streamFailed bool
		var streamAborted bool
		var upstreamEvent string
		var frameData [][]byte
		var miniMaxStreamState *minimaxThinkingStreamState
		if isMiniMaxThinkingTagModel(baseModel) {
			miniMaxStreamState = &minimaxThinkingStreamState{}
		}
		defer streamUsage.Publish(ctx, reporter)

		publishStreamError := func(streamErr statusErr, containsPayload bool) {
			loggedErr := streamErr
			if containsPayload {
				loggedErr = statusErr{code: streamErr.code, msg: "upstream stream returned an error payload"}
			}
			helps.RecordAPIResponseError(ctx, e.cfg, loggedErr)
			reporter.PublishFailure(ctx, loggedErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			streamFailed = true
		}
		if bootstrapErr != nil {
			if se, ok := bootstrapErr.(statusErr); ok {
				publishStreamError(se, true)
			} else {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: bootstrapErr.Error()}, false)
			}
			return
		}
		// Transfer the first bootstrapped data frame into the SSE frame buffer so
		// processFrame handles it before subsequent scanLoop iterations.
		for _, line := range bootstrapLines {
			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("data:")) {
				frameData = append(frameData, bytes.Clone(bytes.TrimSpace(trimmed[len("data:"):])))
			}
		}
		processFrame := func() bool {
			eventName := upstreamEvent
			upstreamEvent = ""
			dataLines := frameData
			frameData = nil
			if len(dataLines) == 0 {
				if openAICompatErrorEvent(eventName) {
					publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream error event ended without data"}, false)
					return true
				}
				return false
			}

			if len(dataLines) > 1 {
				for _, dataLine := range dataLines {
					if bytes.Equal(bytes.TrimSpace(dataLine), []byte("[DONE]")) {
						publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream stream ended with incomplete data before [DONE]"}, false)
						return true
					}
				}
			}
			dataPayload := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
			isDone := bytes.Equal(dataPayload, []byte("[DONE]"))
			if isDone && openAICompatErrorEvent(eventName) {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream error event ended before [DONE]"}, false)
				return true
			}
			if !isDone && !json.Valid(dataPayload) {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream stream ended with incomplete SSE data frame"}, false)
				return true
			}
			if !isDone {
				if streamErr, isError := openAICompatStreamDataError(dataPayload, eventName); isError {
					publishStreamError(streamErr, true)
					return true
				}
			}

			// Meta exposes subscription usage only in a supplemental SSE event
			// on the inference stream. Preserve the stream payload unchanged;
			// normalized internal headers let the conductor persist the
			// observation after this result completes.
			if !isDone && cliproxyauth.IsMetaMuseProvider(e.Identifier()) {
				for name, values := range helps.ParseMetaMuseSubscriptionUsageHeaders(eventName, dataPayload) {
					returnedHeaders[name] = values
				}
			}

			// MiniMax-M3 embeds reasoning tags in delta content. Split them into
			// reasoning_content before reverse translation.
			if miniMaxStreamState != nil {
				dataPayload = normalizeMiniMaxThinkingStream(miniMaxStreamState, dataPayload)
				if isDone {
					if reasoning, content := miniMaxStreamState.flush(); reasoning != "" || content != "" {
						flushFrame := buildMiniMaxThinkingFlushFrame(dataPayload, reasoning, content)
						flushChunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, append([]byte("data: "), flushFrame...), &param, claudeInputTokens)
						for i := range flushChunks {
							select {
							case out <- cliproxyexecutor.StreamChunk{Payload: flushChunks[i]}:
							case <-ctx.Done():
								streamAborted = true
								return true
							}
						}
					}
				}
			}

			streamLine := append([]byte("data: "), dataPayload...)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, streamLine, &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					streamAborted = true
					return true
				}
			}
			if isDone {
				seenDone = true
				return true
			}
			return false
		}

	scanLoop:
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			streamUsage.ObserveOpenAIStream(line)
			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				if processFrame() {
					break scanLoop
				}
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("data:")) {
				frameData = append(frameData, bytes.Clone(bytes.TrimSpace(trimmedLine[len("data:"):])))
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("event:")) {
				upstreamEvent = strings.TrimSpace(string(trimmedLine[len("event:"):]))
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}, true)
				break scanLoop
			}
		}
		errScan := scanner.Err()
		if errScan == nil && !seenDone && !streamFailed && !streamAborted && len(frameData) > 0 {
			_ = processFrame()
		}
		if retriedDeveloperRole && seenDone && !streamFailed && !streamAborted {
			e.rememberDeveloperRoleFallback(developerRoleKey)
		}
		if streamFailed || streamAborted {
			return
		}
		if errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else if !seenDone {
			// Responses clients require an explicit terminal event. Treat a clean
			// upstream EOF without [DONE] as a failed stream instead of completing it.
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				streamErr := statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before [DONE]"}
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.PublishFailure(ctx, streamErr)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
				case <-ctx.Done():
				}
				return
			}

			// Other protocols retain compatibility with providers that omit [DONE].
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		streamUsage.Publish(ctx, reporter)
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: returnedHeaders, Chunks: out}, nil
}

func (e *OpenAICompatExecutor) executeImagesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), true)
	if errPrepare != nil {
		err = errPrepare
		return nil, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	applyOpencodeZenFingerprint(httpReq, e.provider)
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		return nil, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
			reporter.EnsurePublished(ctx)
		}()
		buffer := make([]byte, 32*1024)
		for {
			n, errRead := httpResp.Body.Read(buffer)
			if n > 0 {
				chunk := bytes.Clone(buffer[:n])
				helps.AppendAPIResponseChunk(ctx, e.cfg, chunk)
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, e.cfg, errRead)
					reporter.PublishFailure(ctx, errRead)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: errRead}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	isCompat := helps.APIKeyModelIsCompat(req)
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, false, isCompat)

	modelForCounting := baseModel

	translated, err := helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh is a no-op for API-key based compatibility providers.
// OAuth-style credentials with a refresh token cannot be rotated here; callers
// that need plugin/Home refresh must bind a refresh-capable executor instead.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if openAICompatAuthHasRefreshToken(auth) {
		provider := ""
		if e != nil {
			provider = e.Identifier()
		}
		if provider == "" && auth != nil {
			provider = strings.TrimSpace(auth.Provider)
		}
		return nil, fmt.Errorf("openai compat executor cannot refresh oauth credentials for provider %s", provider)
	}
	return auth, nil
}

func openAICompatAuthHasRefreshToken(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if token, _ := auth.Metadata["refresh_token"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	if token, _ := auth.Metadata["refreshToken"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	return false
}

func openAICompatImageEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != openAICompatImageHandlerType {
		return ""
	}
	path := helps.PayloadRequestPath(opts)
	if strings.HasSuffix(path, "/images/edits") {
		return openAICompatImagesEditsPath
	}
	if strings.HasSuffix(path, "/images/generations") {
		return openAICompatImagesGenerationsPath
	}
	return openAICompatDefaultImageEndpoint
}

func prepareOpenAICompatImagesPayload(payload []byte, model string, contentType string, stream bool) ([]byte, string, error) {
	model = strings.TrimSpace(model)
	contentType = strings.TrimSpace(contentType)
	if json.Valid(payload) {
		if model != "" {
			payload = helps.SetStringIfDifferent(payload, "model", model)
		}
		if stream {
			payload = helps.SetBoolIfDifferent(payload, "stream", true)
		} else {
			payload, _ = sjson.DeleteBytes(payload, "stream")
		}
		return payload, "application/json", nil
	}

	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return payload, contentType, nil
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is missing")
	}
	return rewriteOpenAICompatImagesMultipartPayload(payload, model, boundary, stream)
}

func cloneOpenAICompatMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func rewriteOpenAICompatImagesMultipartPayload(payload []byte, model string, boundary string, stream bool) ([]byte, string, error) {
	reader := multipart.NewReader(bytes.NewReader(payload), boundary)
	form, errRead := reader.ReadForm(openAICompatMultipartMemory)
	if errRead != nil {
		return nil, "", fmt.Errorf("read multipart form failed: %w", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			log.Errorf("openai compat executor: remove multipart form files error: %v", errRemove)
		}
	}()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if model != "" {
		if errWrite := writer.WriteField("model", model); errWrite != nil {
			return nil, "", fmt.Errorf("write model field failed: %w", errWrite)
		}
	}
	if stream {
		if errWrite := writer.WriteField("stream", "true"); errWrite != nil {
			return nil, "", fmt.Errorf("write stream field failed: %w", errWrite)
		}
	}
	for key, values := range form.Value {
		if key == "model" || key == "stream" {
			continue
		}
		for _, value := range values {
			if errWrite := writer.WriteField(key, value); errWrite != nil {
				return nil, "", fmt.Errorf("write form field %s failed: %w", key, errWrite)
			}
		}
	}
	for key, files := range form.File {
		for _, fileHeader := range files {
			if fileHeader == nil {
				continue
			}
			header := cloneOpenAICompatMIMEHeader(fileHeader.Header)
			header.Set("Content-Disposition", multipart.FileContentDisposition(key, fileHeader.Filename))
			if header.Get("Content-Type") == "" {
				header.Set("Content-Type", "application/octet-stream")
			}
			part, errCreate := writer.CreatePart(header)
			if errCreate != nil {
				return nil, "", fmt.Errorf("create file field %s failed: %w", key, errCreate)
			}
			src, errOpen := fileHeader.Open()
			if errOpen != nil {
				return nil, "", fmt.Errorf("open upload file failed: %w", errOpen)
			}
			_, errCopy := io.Copy(part, src)
			if errClose := src.Close(); errClose != nil {
				log.Errorf("openai compat executor: close upload file error: %v", errClose)
				if errCopy == nil {
					errCopy = errClose
				}
			}
			if errCopy != nil {
				return nil, "", fmt.Errorf("copy upload file failed: %w", errCopy)
			}
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, "", fmt.Errorf("close multipart writer failed: %w", errClose)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func (e *OpenAICompatExecutor) applyPromptCacheKey(ctx context.Context, auth *cliproxyauth.Auth, from sdktranslator.Format, baseModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, translated []byte) ([]byte, error) {
	compat := e.resolveCompatConfig(auth)
	if compat == nil || !compat.SupportPromptCacheKey {
		return translated, nil
	}

	for _, payload := range [][]byte{req.Payload, opts.OriginalRequest, translated} {
		if promptCacheKey := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); promptCacheKey != "" {
			return helps.SetStringIfDifferent(translated, "prompt_cache_key", promptCacheKey), nil
		}
	}

	modelName := strings.TrimSpace(gjson.GetBytes(translated, "model").String())
	if modelName == "" {
		modelName = baseModel
	}
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		cached, ok, errCache := helps.ClaudeCodePromptCache(ctx, modelName, req.Payload, opts.Headers)
		if errCache != nil {
			return translated, errCache
		}
		if ok {
			return helps.SetStringIfDifferent(translated, "prompt_cache_key", cached.ID), nil
		}
	}

	sessionID := helps.ProviderSessionUUID(e.provider, opts.Metadata, req.Metadata)
	if sessionID == "" {
		return translated, nil
	}
	provider := strings.TrimSpace(e.provider)
	if provider == "" {
		provider = strings.TrimSpace(compat.Name)
	}
	identity := strings.Join([]string{
		"cli-proxy-api:openai-compat:prompt-cache",
		strings.ToLower(provider),
		strings.ToLower(modelName),
		strings.ToLower(strings.TrimSpace(from.String())),
		sessionID,
	}, "\x00")
	promptCacheKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
	return helps.SetStringIfDifferent(translated, "prompt_cache_key", promptCacheKey), nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	if auth.AuthSourceKind() == cliproxyauth.AuthSourceConfig && auth.Attributes != nil {
		if rawIndex := strings.TrimSpace(auth.Attributes["config_index"]); rawIndex != "" {
			configIndex, errIndex := strconv.Atoi(rawIndex)
			if errIndex == nil && configIndex >= 0 && configIndex < len(e.cfg.OpenAICompatibility) {
				compat := &e.cfg.OpenAICompatibility[configIndex]
				if !compat.Disabled {
					return compat
				}
			}
		}
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func stripOpenAICompatProviderUnsupportedFields(provider string, compat *config.OpenAICompatibility, payload []byte) []byte {
	if isKimiOpenAICompatProvider(provider, compat) {
		payload = normalizeKimiMessageRoles(stripKimiUnsupportedFields(payload))
		return stripKimiFixedSamplingFields(payload, gjson.GetBytes(payload, "model").String())
	}
	return payload
}

func isKimiOpenAICompatProvider(provider string, compat *config.OpenAICompatibility) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if strings.Contains(provider, "kimi") || strings.Contains(provider, "moonshot") {
		return true
	}
	if compat == nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(compat.Name))
	if strings.Contains(name, "kimi") || strings.Contains(name, "moonshot") {
		return true
	}
	baseURL := strings.ToLower(strings.TrimSpace(compat.BaseURL))
	return strings.Contains(baseURL, "moonshot") || strings.Contains(baseURL, "kimi")
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	return helps.SetStringIfDifferent(payload, "model", model)
}

// Xiaomi MiMo-V series model that requires reasoning_content preservation in
// multi-turn tool-call conversations.
// Matching pattern: model name contains "mimo-v" (case-insensitive).
func isMiMoModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "mimo-v")
}

// isDeepSeekModel reports whether the model name refers to a DeepSeek model.
func isDeepSeekModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "deepseek")
}

// stripDeepSeekUnsupportedFields removes DeepSeek-specific top-level fields that are
// not supported by generic OpenAI-compatible providers.
// Fields removed: reasoning, reasoningSummary, include, verbosity, interleaved, reasoning_effort
// The "thinking" field is preserved as it's used by the thinking system.
func stripDeepSeekUnsupportedFields(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	fields := []string{"reasoning", "reasoningSummary", "include", "verbosity", "interleaved", "reasoning_effort"}
	out := body
	for _, field := range fields {
		if gjson.GetBytes(out, field).Exists() {
			updated, err := sjson.DeleteBytes(out, field)
			if err == nil {
				out = updated
			}
		}
	}
	return out
}

// isMistralProvider reports whether the provider identifier corresponds to Mistral.
func isMistralProvider(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	return p == "mistral" || p == "mistral.ai"
}

// normalizeMistralReasoningEffort forces reasoning_effort to "high" for models
// whose name contains "mistral". Mistral models only accept "high" or "none"
// and reject values like "medium" or "low".
func normalizeMistralReasoningEffort(model string, body []byte) []byte {
	if !strings.Contains(strings.ToLower(strings.TrimSpace(model)), "mistral") {
		return body
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	effort := gjson.GetBytes(body, "reasoning_effort")
	if !effort.Exists() || effort.String() == "high" {
		return body
	}
	if updated, err := sjson.SetBytes(body, "reasoning_effort", "high"); err == nil {
		return updated
	}
	return body
}

// resolveReasoningEffortConflict resolves conflicts between the "reasoning" object
// and the "reasoning_effort" string field. When both exist, "reasoning" is removed
// to avoid sending conflicting signals to the upstream provider.
func resolveReasoningEffortConflict(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	hasReasoning := gjson.GetBytes(body, "reasoning").Exists()
	hasReasoningEffort := gjson.GetBytes(body, "reasoning_effort").Exists()
	if hasReasoning && hasReasoningEffort {
		updated, err := sjson.DeleteBytes(body, "reasoning")
		if err == nil {
			return updated
		}
	}
	return body
}

// isOpenCodeZenProvider reports whether the base URL points to the
// opencode.ai/zen/ gateway. The Zen gateway routes to upstream providers
// (e.g. MiniMax) that only accept "adaptive" or "disabled" for thinking.type
// and reject "enabled".
func isOpenCodeZenProvider(baseURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(lower, "opencode.ai/zen/")
}

// needsToolCallIDNormalization reports whether the model requires 9-char alphanumeric
// tool call ID normalization (NVIDIA, Mistral-Medium-3.5).
func needsToolCallIDNormalization(model string, compat *config.OpenAICompatibility) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(lower, "mistral-medium-3.5") || strings.Contains(lower, "mistralai/") {
		return true
	}
	if compat != nil {
		name := strings.ToLower(compat.Name)
		if strings.Contains(name, "nvidia") || strings.Contains(name, "nvapi") {
			return true
		}
	}
	return false
}

// isNVIDIACompatProvider reports whether the provider is an NVIDIA endpoint.
func isNVIDIACompatProvider(compat *config.OpenAICompatibility) bool {
	if compat == nil {
		return false
	}
	name := strings.ToLower(compat.Name)
	return strings.Contains(name, "nvidia") || strings.Contains(name, "nvapi")
}

// applyNVIDIAMaxTokensReduction reduces max_tokens by 2 for NVIDIA endpoints.
// NVIDIA reserves 2 tokens for internal use.
func applyNVIDIAMaxTokensReduction(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	mt := gjson.GetBytes(body, "max_tokens")
	if !mt.Exists() || mt.Int() < 3 {
		return body
	}
	next, err := sjson.SetBytes(body, "max_tokens", mt.Int()-2)
	if err != nil {
		return body
	}
	return next
}

// SenseNova rejects the whole request with 400 when max_tokens falls outside this range.
const (
	sensenovaMinMaxTokens int64 = 1
	sensenovaMaxMaxTokens int64 = 65536
)

// isSensenovaCompatProvider reports whether the provider is a SenseNova endpoint.
func isSensenovaCompatProvider(compat *config.OpenAICompatibility) bool {
	if compat == nil {
		return false
	}
	return strings.Contains(strings.ToLower(compat.Name), "sensenova")
}

// applySensenovaMaxTokensClamp clamps max_tokens into the range SenseNova accepts.
// A non-numeric or absent max_tokens is left untouched so the upstream default applies.
func applySensenovaMaxTokensClamp(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	mt := gjson.GetBytes(body, "max_tokens")
	if mt.Type != gjson.Number {
		return body
	}
	requested := mt.Int()
	clamped := requested
	if clamped < sensenovaMinMaxTokens {
		clamped = sensenovaMinMaxTokens
	}
	if clamped > sensenovaMaxMaxTokens {
		clamped = sensenovaMaxMaxTokens
	}
	if clamped == requested {
		return body
	}
	next, err := sjson.SetBytes(body, "max_tokens", clamped)
	if err != nil {
		return body
	}
	return next
}

// sanitizeEmptyToolFunctionNames drops malformed tool declarations and tool-call
// references across every wire shape this executor can emit to an
// OpenAI-compatible upstream:
//
//   - tools[].function.name (Chat Completions), tools[].name (flat/Responses-style),
//     and tools[].custom.name (Responses "custom" tool variant). The effective name is
//     the first non-empty of function.name / name / custom.name; entries whose effective
//     name is empty or whitespace are dropped. Missing/empty function.arguments on kept
//     entries with a function object are backfilled with "{}". If no tools remain the
//     tools field is removed entirely.
//   - messages[].tool_calls[].function.name (Chat Completions assistant replay): entries
//     with an empty/whitespace function name are dropped, mirroring sanitizeSensenovaToolCalls
//     but applied unconditionally so every openai-compatible provider is protected.
//   - input[] items shaped like the Responses API (type function_call / custom_tool_call)
//     whose name is empty or whitespace are dropped.
//
// Some OpenAI-compatible routers (e.g. Upstage Solar and other gateways behind the
// minpeter proxy) reject such requests with `Invalid function name: ”` or “ `name`
// must be non-empty “, which silently breaks otherwise valid tool calls/tool catalogs.
func sanitizeEmptyToolFunctionNames(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	body = sanitizeEmptyToolDeclarations(body)
	body = sanitizeEmptyToolCallMessages(body)
	body = sanitizeEmptyResponsesFunctionCallItems(body)
	return body
}

// toolEffectiveName resolves a tool declaration's name across every wire shape:
// the nested Chat Completions form (function.name), the flat Responses-style form
// (name), and the Responses "custom" tool form (custom.name). The first non-empty
// candidate wins.
func toolEffectiveName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("function.name").String()); name != "" {
		return name
	}
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("custom.name").String())
}

// sanitizeEmptyToolDeclarations drops tools[] entries whose effective name (see
// toolEffectiveName) is empty or whitespace, and backfills missing or empty
// function.arguments with "{}" on entries that carry a function object. If every
// entry is dropped, the tools field itself is removed.
func sanitizeEmptyToolDeclarations(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}

	out := body
	kept := make([]string, 0, len(tools.Array()))
	changed := false
	for _, tool := range tools.Array() {
		if toolEffectiveName(tool) == "" {
			changed = true
			continue
		}
		raw := tool.Raw
		if tool.Get("function").Exists() {
			if args := tool.Get("function.arguments"); !args.Exists() || strings.TrimSpace(args.String()) == "" {
				repaired, errSet := sjson.Set(raw, "function.arguments", "{}")
				if errSet != nil {
					return body
				}
				raw = repaired
				changed = true
			}
		}
		kept = append(kept, raw)
	}
	if !changed {
		return body
	}
	if len(kept) == 0 {
		next, err := sjson.DeleteBytes(out, "tools")
		if err != nil {
			return body
		}
		return next
	}
	next, err := sjson.SetRawBytes(out, "tools", []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return body
	}
	return next
}

// sanitizeEmptyToolCallMessages drops messages[].tool_calls[] entries whose
// function.name is empty or whitespace. Unlike sanitizeSensenovaToolCalls, it does
// not attempt orphaned tool-result cleanup and applies unconditionally to every
// openai-compatible provider, not just SenseNova.
func sanitizeEmptyToolCallMessages(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}

	out := body
	for msgIdx, msg := range messages.Array() {
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.IsArray() {
			continue
		}
		calls := toolCalls.Array()
		kept := make([]string, 0, len(calls))
		changed := false
		for _, call := range calls {
			if strings.TrimSpace(call.Get("function.name").String()) == "" {
				changed = true
				continue
			}
			kept = append(kept, call.Raw)
		}
		if !changed {
			continue
		}
		path := fmt.Sprintf("messages.%d.tool_calls", msgIdx)
		var next []byte
		var err error
		if len(kept) == 0 {
			next, err = sjson.DeleteBytes(out, path)
		} else {
			next, err = sjson.SetRawBytes(out, path, []byte("["+strings.Join(kept, ",")+"]"))
		}
		if err != nil {
			return body
		}
		out = next
	}
	return out
}

// dropOrphanOpenAIToolResults removes `role:"tool"` messages whose tool_call_id
// has no matching tool_calls[].id in any assistant message. Strict upstreams
// (e.g. MiniMax) reject the whole request with 400 error 2013 — "tool result's
// tool id ... not found" — when the replayed history contains such an orphan,
// typically produced when a sanitizer drops a tool call while its paired
// result message survives, or when history is replayed across a provider
// switch. Orphans are removed in place; paired results are untouched. Results
// with an empty tool_call_id are kept because some clients legitimately omit
// the id and upstreams tolerate it.
func dropOrphanOpenAIToolResults(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}

	msgs := messages.Array()
	known := make(map[string]struct{})
	for _, msg := range msgs {
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.IsArray() {
			continue
		}
		for _, call := range toolCalls.Array() {
			if id := strings.TrimSpace(call.Get("id").String()); id != "" {
				known[id] = struct{}{}
			}
		}
	}

	out := body
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if !strings.EqualFold(msg.Get("role").String(), "tool") {
			continue
		}
		id := strings.TrimSpace(msg.Get("tool_call_id").String())
		if id == "" {
			continue
		}
		if _, ok := known[id]; ok {
			continue
		}
		next, err := sjson.DeleteBytes(out, fmt.Sprintf("messages.%d", i))
		if err != nil {
			return body
		}
		out = next
	}
	return out
}

// sanitizeEmptyResponsesFunctionCallItems drops input[] items shaped like the
// Responses API function-call replay (type function_call / custom_tool_call) whose
// name is empty or whitespace. Other input item types are left untouched.
func sanitizeEmptyResponsesFunctionCallItems(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}

	items := input.Array()
	kept := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		itemType := strings.TrimSpace(item.Get("type").String())
		if (itemType == "function_call" || itemType == "custom_tool_call") && strings.TrimSpace(item.Get("name").String()) == "" {
			changed = true
			continue
		}
		kept = append(kept, item.Raw)
	}
	if !changed {
		return body
	}
	if len(kept) == 0 {
		next, err := sjson.DeleteBytes(body, "input")
		if err != nil {
			return body
		}
		return next
	}
	next, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return body
	}
	return next
}

// sanitizeSensenovaToolCalls repairs the tool_calls SenseNova rejects with 400: an entry
// without a function name is dropped, and empty or missing arguments become "{}". A message
// left without any tool call loses the field entirely. Every other field is preserved.
func sanitizeSensenovaToolCalls(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}

	// Pass 1: drop tool calls with an empty/whitespace function name and
	// remember their ids so the orphaned tool result messages can be removed.
	droppedIDs := map[string]bool{}
	out := body
	for msgIdx, msg := range messages.Array() {
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.IsArray() {
			continue
		}
		calls := toolCalls.Array()
		kept := make([]string, 0, len(calls))
		changed := false
		for _, call := range calls {
			if strings.TrimSpace(call.Get("function.name").String()) == "" {
				if id := call.Get("id").String(); id != "" {
					droppedIDs[id] = true
				}
				changed = true
				continue
			}
			raw := call.Raw
			if args := call.Get("function.arguments"); !args.Exists() || strings.TrimSpace(args.String()) == "" {
				repaired, errSet := sjson.Set(raw, "function.arguments", "{}")
				if errSet != nil {
					return body
				}
				raw = repaired
				changed = true
			}
			kept = append(kept, raw)
		}
		if !changed {
			continue
		}
		path := fmt.Sprintf("messages.%d.tool_calls", msgIdx)
		var next []byte
		var err error
		if len(kept) == 0 {
			next, err = sjson.DeleteBytes(out, path)
		} else {
			next, err = sjson.SetRawBytes(out, path, []byte("["+strings.Join(kept, ",")+"]"))
		}
		if err != nil {
			return body
		}
		out = next
	}

	// Pass 2: drop tool result messages whose tool_call_id references a dropped
	// tool call. Otherwise the upstream rejects the orphaned output with
	// "No function call found for function call output with call_id ...".
	if len(droppedIDs) == 0 {
		return out
	}
	updated := gjson.GetBytes(out, "messages")
	if !updated.IsArray() {
		return out
	}
	msgs := updated.Array()
	removed := false
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if !strings.EqualFold(msg.Get("role").String(), "tool") {
			continue
		}
		if !droppedIDs[msg.Get("tool_call_id").String()] {
			continue
		}
		var err error
		out, err = sjson.DeleteBytes(out, fmt.Sprintf("messages.%d", i))
		if err != nil {
			return body
		}
		removed = true
	}
	if !removed {
		return out
	}
	return out
}

// applyMiMoReasoningBackfill ensures reasoning_content is preserved or backfilled
// for assistant messages that contain tool_calls, as required by Xiaomi MiMo-V models.
// See: https://platform.xiaomimimo.com/docs/en-US/usage-guide/passing-back-reasoning_content
func applyMiMoReasoningBackfill(body []byte) []byte {
	out, _, err := normalizeOpenAIToolCallReasoningContentWithOptions(body, openAIReasoningNormalizationOptions{
		forceForProvider:     true,
		requireExistingChain: false,
	})
	if err != nil {
		log.Debugf("openai compat executor: mimo reasoning backfill error: %v", err)
		return body
	}
	return out
}

// normalizeDeltaContentArray normalizes SSE streaming delta content.
// When delta.content is an array (e.g., from providers that send content parts),
// extracts only "text" type parts and joins them into a single string,
// stripping "thinking" and other non-text parts.
func normalizeDeltaContentArray(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return line
	}
	// Leave non-data lines unchanged
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return line
	}
	jsonPart := trimmed[len("data:"):]
	jsonTrimmed := bytes.TrimSpace(jsonPart)
	// Leave [DONE] unchanged
	if string(jsonTrimmed) == "[DONE]" {
		return line
	}
	if !gjson.ValidBytes(jsonTrimmed) {
		return line
	}
	choices := gjson.GetBytes(jsonTrimmed, "choices")
	if !choices.Exists() || !choices.IsArray() || len(choices.Array()) == 0 {
		return line
	}

	modified := false
	out := jsonTrimmed
	for ci, choice := range choices.Array() {
		content := choice.Get("delta.content")
		if !content.Exists() {
			continue
		}
		// String content: leave as-is
		if content.Type == gjson.String {
			continue
		}
		// Array content: normalize
		if !content.IsArray() {
			continue
		}
		var textParts []string
		for _, item := range content.Array() {
			itemType := item.Get("type").String()
			if itemType == "text" {
				t := item.Get("text").String()
				if t != "" {
					textParts = append(textParts, t)
				}
			}
			// Skip "thinking" and other non-text types
		}
		combined := strings.Join(textParts, "")
		next, err := sjson.SetBytes(out, fmt.Sprintf("choices.%d.delta.content", ci), combined)
		if err != nil {
			continue
		}
		out = next
		modified = true
	}
	if !modified {
		return line
	}
	prefix := "data: "
	return append([]byte(prefix), out...)
}

// fixMistralMessageOrder ensures the last message before a new user turn
// has proper Mistral prefix semantics:
//   - If the last message is assistant without a prefix field → add prefix=true
//   - If the last message is assistant with prefix=false → append a placeholder user message
//   - Otherwise → leave unchanged
func (e *OpenAICompatExecutor) fixMistralMessageOrder(payload []byte) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() || len(messages.Array()) == 0 {
		return payload
	}
	msgs := messages.Array()
	lastMsg := msgs[len(msgs)-1]
	lastRole := strings.TrimSpace(lastMsg.Get("role").String())
	if lastRole != "assistant" {
		return payload
	}
	prefix := lastMsg.Get("prefix")
	if prefix.Exists() {
		if prefix.Type == gjson.True {
			// Already has prefix=true
			return payload
		}
		// prefix=false: append placeholder user message
		placeholder := []byte(`{"role":"user","content":"."}`)
		existing := messages.Raw
		newMessages := existing[:len(existing)-1] + "," + string(placeholder) + "]"
		next, err := sjson.SetRawBytes(payload, "messages", []byte(newMessages))
		if err != nil {
			return payload
		}
		return next
	}
	// No prefix field: add prefix=true to last assistant message
	next, err := sjson.SetBytes(payload, fmt.Sprintf("messages.%d.prefix", len(msgs)-1), true)
	if err != nil {
		return payload
	}
	return next
}

// normalizeToolResultIDsToString ensures all tool-related id fields in the
// request payload are JSON strings.  Some clients send numeric or null id
// values which upstream providers (e.g. north-mini-code-free) reject with
// "A tool result's output's id field must be a string".
//
// The function inspects both the chat-completions format (messages array)
// and the responses format (input array) and coerces non-string id/call_id/
// tool_call_id fields to their string representation via sjson.
func normalizeToolResultIDsToString(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	out := body

	// --- Chat completions format: messages array ---
	messages := gjson.GetBytes(out, "messages")
	if messages.Exists() && messages.IsArray() {
		for msgIdx, msg := range messages.Array() {
			// tool role messages: ensure tool_call_id is a string
			if msg.Get("role").String() == "tool" {
				tcID := msg.Get("tool_call_id")
				if tcID.Exists() && tcID.Type != gjson.String {
					path := fmt.Sprintf("messages.%d.tool_call_id", msgIdx)
					out, _ = sjson.SetBytes(out, path, tcID.String())
				}
			}
			// assistant messages with tool_calls: ensure each tool call id is a string
			toolCalls := msg.Get("tool_calls")
			if toolCalls.Exists() && toolCalls.IsArray() {
				for callIdx, call := range toolCalls.Array() {
					idVal := call.Get("id")
					if idVal.Exists() && idVal.Type != gjson.String {
						path := fmt.Sprintf("messages.%d.tool_calls.%d.id", msgIdx, callIdx)
						out, _ = sjson.SetBytes(out, path, idVal.String())
					}
				}
			}
		}
	}

	// --- Responses format: input array ---
	input := gjson.GetBytes(out, "input")
	if input.Exists() && input.IsArray() {
		for itemIdx, item := range input.Array() {
			itemType := item.Get("type").String()
			if itemType == "function_call" || itemType == "function_call_output" {
				for _, field := range []string{"id", "call_id"} {
					val := item.Get(field)
					if val.Exists() && val.Type != gjson.String {
						path := fmt.Sprintf("input.%d.%s", itemIdx, field)
						out, _ = sjson.SetBytes(out, path, val.String())
					}
				}
			}
		}
	}

	return out
}

func openAICompatErrorEvent(eventName string) bool {

	return strings.EqualFold(eventName, "error") || strings.EqualFold(eventName, "response.error") || strings.EqualFold(eventName, "response.failed")
}

var openAICompatBracketedStatusRe = regexp.MustCompile(`\[(4[0-9]{2}|5[0-9]{2})\]`)

// openAICompatEmbeddedStatusFromMessage recovers an upstream status embedded in the
// error message text (e.g. "Streaming response failed: [504] Upstream idle timeout
// exceeded"). Gateways that surface upstream failures as in-stream error events often
// omit a machine-readable status field; without this recovery the conductor only sees
// the 502 fallback guess and consumes the retry budget instead of retrying same-model
// credentials first for budget-preserving statuses (429/503/504).
func openAICompatEmbeddedStatusFromMessage(payload []byte) int {
	for _, path := range []string{"error.message", "response.error.message", "message"} {
		message := gjson.GetBytes(payload, path).String()
		if message == "" {
			continue
		}
		for _, match := range openAICompatBracketedStatusRe.FindAllString(message, -1) {
			status, errConv := strconv.Atoi(strings.Trim(match, "[]"))
			if errConv == nil && status >= http.StatusBadRequest && status <= 599 {
				return status
			}
		}
	}
	return 0
}

func openAICompatStreamDataError(payload []byte, eventName string) (statusErr, bool) {
	if len(payload) == 0 || !json.Valid(payload) {
		return statusErr{}, false
	}
	payloadType := gjson.GetBytes(payload, "type").String()
	hasError := false
	for _, path := range []string{"error", "response.error"} {
		errorNode := gjson.GetBytes(payload, path)
		if errorNode.Exists() && errorNode.Raw != "null" {
			hasError = true
			break
		}
	}
	hasTopLevelErrorFields := gjson.GetBytes(payload, "code").Exists() && gjson.GetBytes(payload, "message").Exists()
	if !hasError && !strings.EqualFold(payloadType, "error") && !strings.EqualFold(payloadType, "response.error") && !strings.EqualFold(payloadType, "response.failed") &&
		!openAICompatErrorEvent(eventName) && !hasTopLevelErrorFields {
		return statusErr{}, false
	}

	status := 0
	for _, path := range []string{"status", "status_code", "error.status", "error.status_code", "response.error.status", "response.error.status_code"} {
		status = int(gjson.GetBytes(payload, path).Int())
		if status >= http.StatusBadRequest && status <= 599 {
			break
		}
	}
	if status < http.StatusBadRequest || status > 599 {
		status = openAICompatEmbeddedStatusFromMessage(payload)
	}
	if status < http.StatusBadRequest || status > 599 {
		status = http.StatusBadGateway
	}
	return statusErr{code: status, msg: string(payload)}, true
}

type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }

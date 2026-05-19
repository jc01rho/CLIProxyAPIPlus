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
	"strconv"
	"strings"
	"time"

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

const nvidiaCompatTokenReserve int64 = 2

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
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
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
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), opts.Stream)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
	}
	translated = e.applyCompatSafetyMargin(auth, translated)
	translated, err = e.normalizeToolCallReasoningContentWithAuth(auth, translated)
	if err != nil {
		return resp, err
	}
	translated = e.stripProviderUnsupportedFields(auth, baseModel, translated)
	translated, err = e.normalizeProviderToolCallIDs(auth, baseModel, translated)
	if err != nil {
		return resp, err
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
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
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

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logDetailedAPIError(ctx, e.cfg, e.Identifier(), baseModel, url, httpResp.StatusCode, httpResp.Header.Get("Content-Type"), translated, b)
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, body)
	reporter.publish(ctx, parseOpenAIUsage(body))
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.ensurePublished(ctx)
	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
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
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
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

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}
	translated = e.applyCompatSafetyMargin(auth, translated)

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)
	translated, err = e.normalizeToolCallReasoningContentWithAuth(auth, translated)
	if err != nil {
		return nil, err
	}
	translated = e.stripProviderUnsupportedFields(auth, baseModel, translated)
	translated, err = e.normalizeProviderToolCallIDs(auth, baseModel, translated)
	if err != nil {
		return nil, err
	}

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
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
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

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logDetailedAPIError(ctx, e.cfg, e.Identifier(), baseModel, url, httpResp.StatusCode, httpResp.Header.Get("Content-Type"), translated, b)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				continue
			}

			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				if bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("event:")) ||
					bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
					continue
				}
				if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
					streamErr := statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)

					reporter.publishFailure(ctx)

					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				continue
			}

			// OpenAI-compatible streams must use SSE data lines.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(normalizeDeltaContentArray(trimmedLine)), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)

			reporter.publishFailure(ctx)

			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else {
			// In case the upstream close the stream without a terminal [DONE] marker.
			// Feed a synthetic done marker through the translator so pending
			// response.completed events are still emitted exactly once.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		// Ensure we record the request if no usage chunk was ever seen
		reporter.ensurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) executeImagesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
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
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
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
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	modelForCounting := baseModel

	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := tokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := countOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := buildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh is a no-op for API-key based compatibility providers.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
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
			payload, _ = sjson.SetBytes(payload, "model", model)
		}
		if stream {
			payload, _ = sjson.SetBytes(payload, "stream", true)
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

func (e *OpenAICompatExecutor) applyCompatSafetyMargin(auth *cliproxyauth.Auth, payload []byte) []byte {
	compat := e.resolveCompatConfig(auth)
	if compat == nil || !strings.EqualFold(strings.TrimSpace(compat.Name), "nvidia-nvapi") {
		return payload
	}

	maxTokens := gjson.GetBytes(payload, "max_tokens")
	if !maxTokens.Exists() {
		return payload
	}

	current := maxTokens.Int()
	if current <= nvidiaCompatTokenReserve {
		return payload
	}

	updated, err := sjson.SetBytes(payload, "max_tokens", current-nvidiaCompatTokenReserve)
	if err != nil {
		return payload
	}
	return updated
}

func (e *OpenAICompatExecutor) normalizeToolCallReasoningContentWithAuth(auth *cliproxyauth.Auth, payload []byte) ([]byte, error) {
	providerName := strings.ToLower(strings.TrimSpace(e.provider))
	compatName := providerName
	if compat := e.resolveCompatConfig(auth); compat != nil && strings.TrimSpace(compat.Name) != "" {
		compatName = strings.ToLower(strings.TrimSpace(compat.Name))
	}
	if auth != nil {
		authProvider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if authProvider != "" {
			providerName = authProvider
		}
	}
	isMistral := compatName == "mistral.ai" || providerName == "mistral.ai"
	isXiaomi := strings.HasPrefix(compatName, "xiaomi") || strings.HasPrefix(providerName, "xiaomi")
	forceReasoningReplay := isMistral || isXiaomi
	requireExistingChain := isMistral
	updated, patched, err := normalizeOpenAIToolCallReasoningContentWithOptions(payload, openAIReasoningNormalizationOptions{
		requireReasoningSignal: true,
		forceForProvider:       forceReasoningReplay,
		requireExistingChain:   requireExistingChain,
	})
	if err != nil {
		return payload, fmt.Errorf("openai compat executor: normalize reasoning_content: %w", err)
	}
	if patched > 0 {
		log.WithFields(log.Fields{
			"patched_reasoning_messages": patched,
			"provider":                  compatName,
		}).Debug("openai compat executor: normalized tool-call reasoning_content")
	}
	return updated, nil
}

func (e *OpenAICompatExecutor) stripProviderUnsupportedFields(auth *cliproxyauth.Auth, model string, payload []byte) []byte {
	compatName := ""
	if compat := e.resolveCompatConfig(auth); compat != nil {
		compatName = strings.ToLower(strings.TrimSpace(compat.Name))
	}
	providerName := strings.ToLower(strings.TrimSpace(e.provider))
	if auth != nil {
		if authProvider := strings.ToLower(strings.TrimSpace(auth.Provider)); authProvider != "" {
			providerName = authProvider
		}
	}
	baseURL, _ := e.resolveCredentials(auth)
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	upstreamModel := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if upstreamModel == "" {
		upstreamModel = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "model").String()))
		upstreamModel = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(upstreamModel).ModelName))
	}
	upstreamModelLeaf := upstreamModel
	if slash := strings.LastIndex(upstreamModelLeaf, "/"); slash >= 0 {
		upstreamModelLeaf = upstreamModelLeaf[slash+1:]
	}

	isMistral := compatName == "mistral.ai" || providerName == "mistral.ai"
	isDeepSeekLike := strings.Contains(baseURL, "api.deepseek.com") || strings.Contains(baseURL, "nano-gpt.com") ||
		strings.HasPrefix(upstreamModel, "deepseek") || strings.Contains(upstreamModel, "/deepseek") ||
		strings.HasPrefix(upstreamModelLeaf, "deepseek") ||
		compatName == "nanogpt" || providerName == "nanogpt"

	shouldStripReasoning := gjson.GetBytes(payload, "reasoning").Exists() &&
		(gjson.GetBytes(payload, "reasoning_effort").Exists() || isMistral || isDeepSeekLike)
	if shouldStripReasoning {
		updated, err := sjson.DeleteBytes(payload, "reasoning")
		if err == nil {
			payload = updated
		}
	}

	if !isMistral && !isDeepSeekLike {
		return payload
	}

	paths := []string{"reasoning", "reasoningSummary", "include", "verbosity", "interleaved", "reasoning_effort"}
	if isMistral {
		paths = append(paths, "thinking")
	}
	for _, path := range paths {
		updated, err := sjson.DeleteBytes(payload, path)
		if err == nil {
			payload = updated
		}
	}
	if isDeepSeekLike {
		// DeepSeek/nano-gpt rejects tool parameter schemas that contain $schema meta-keys.
		// Strip tools[*].function.parameters.$schema for all DeepSeek-like upstreams.
		tools := gjson.GetBytes(payload, "tools")
		if tools.Exists() && tools.IsArray() {
			for idx := range tools.Array() {
				path := "tools." + strconv.Itoa(idx) + ".function.parameters.$schema"
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel == nil {
					payload = updated
				}
			}
		}
	}
	if !isMistral {
		return payload
	}
	messages := gjson.GetBytes(payload, "messages")
	if messages.Exists() && messages.IsArray() {
		msgArray := messages.Array()
		kept := make([]string, 0, len(msgArray))
		dropped := 0
		for idx, msg := range msgArray {
			if strings.TrimSpace(msg.Get("role").String()) == "assistant" {
				path := "messages." + strconv.Itoa(idx) + ".reasoning_content"
				updated, err := sjson.DeleteBytes(payload, path)
				if err == nil {
					payload = updated
				}
			}
		}
		messages = gjson.GetBytes(payload, "messages")
		if messages.Exists() && messages.IsArray() {
			for _, msg := range messages.Array() {
				if shouldDropEmptyAssistantMessage(msg) {
					dropped++
					continue
				}
				kept = append(kept, msg.Raw)
			}
			if dropped > 0 {
				rawMessages := []byte("[" + strings.Join(kept, ",") + "]")
				next, err := sjson.SetRawBytes(payload, "messages", rawMessages)
				if err == nil {
					payload = next
				}
				log.WithField("dropped_assistant_messages", dropped).Debug("openai compat: dropped empty assistant messages for Mistral")
			}
		}
	}
	payload = e.fixMistralMessageOrder(payload)
	return payload
}

func (e *OpenAICompatExecutor) fixMistralMessageOrder(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}
	msgArray := messages.Array()
	if len(msgArray) == 0 {
		return payload
	}
	lastMsg := msgArray[len(msgArray)-1]
	lastRole := strings.TrimSpace(lastMsg.Get("role").String())
	if lastRole == "assistant" {
		if !lastMsg.Get("prefix").Exists() {
			updated, err := sjson.SetBytes(payload, "messages."+strconv.Itoa(len(msgArray)-1)+".prefix", true)
			if err == nil {
				log.Debug("openai compat: added prefix=true to last assistant message for Mistral message order")
				return updated
			}
		}
		if lastMsg.Get("prefix").Bool() {
			return payload
		}
		placeholderUser := []byte(`{"role":"user","content":"."}`)
		payload, _ = sjson.SetRawBytes(payload, "messages.-1", placeholderUser)
		log.Debug("openai compat: appended placeholder user message for Mistral message order")
	}
	return payload
}

func shouldDropEmptyAssistantMessage(msg gjson.Result) bool {
	if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
		return false
	}
	toolCalls := msg.Get("tool_calls")
	if toolCalls.Exists() && toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
		return false
	}
	functionCall := msg.Get("function_call")
	if functionCall.Exists() && functionCall.Type != gjson.Null {
		if functionCall.IsObject() && strings.TrimSpace(functionCall.Raw) != "{}" {
			return false
		}
	}
	content := msg.Get("content")
	if !content.Exists() || content.Type == gjson.Null {
		return true
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String()) == ""
	}
	if content.IsArray() {
		for _, part := range content.Array() {
			if part.Exists() && part.Type != gjson.Null {
				if part.Type == gjson.String && strings.TrimSpace(part.String()) != "" {
					return false
				}
				if part.IsObject() && strings.TrimSpace(part.Raw) != "{}" && strings.TrimSpace(part.Raw) != "null" {
					return false
				}
			}
		}
		return true
	}
	return false
}

func (e *OpenAICompatExecutor) normalizeProviderToolCallIDs(auth *cliproxyauth.Auth, model string, payload []byte) ([]byte, error) {
	compatName := ""
	if compat := e.resolveCompatConfig(auth); compat != nil {
		compatName = strings.TrimSpace(compat.Name)
	}
	providerName := strings.TrimSpace(e.provider)
	if auth != nil {
		if authProvider := strings.TrimSpace(auth.Provider); authProvider != "" {
			providerName = authProvider
		}
	}
	upstreamModel := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if upstreamModel == "" {
		upstreamModel = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "model").String()))
		upstreamModel = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(upstreamModel).ModelName))
	}
	upstreamModelLeaf := upstreamModel
	if slash := strings.LastIndex(upstreamModelLeaf, "/"); slash >= 0 {
		upstreamModelLeaf = upstreamModelLeaf[slash+1:]
	}
	shouldNormalize := strings.EqualFold(strings.TrimSpace(compatName), "nvidia-nvapi") ||
		strings.EqualFold(strings.TrimSpace(providerName), "nvidia-nvapi") ||
		strings.HasPrefix(upstreamModelLeaf, "mistral-medium-3.5")
	if !shouldNormalize {
		return payload, nil
	}
	updated, patched, err := normalizeNVIDIAToolCallIDs(payload)
	if err != nil {
		return payload, fmt.Errorf("openai compat executor: normalize provider tool call ids: %w", err)
	}
	if patched > 0 {
		log.WithFields(log.Fields{
			"patched_tool_call_ids": patched,
			"provider":              strings.TrimSpace(compatName),
			"executor_provider":     strings.TrimSpace(providerName),
			"upstream_model":        upstreamModel,
		}).Debug("openai compat executor: normalized provider tool call ids")
	}
	return updated, nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
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

func normalizeDeltaContentArray(line []byte) []byte {
	const prefix = "data: "
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return line
	}
	jsonPart := bytes.TrimSpace(line[len(prefix):])
	if len(jsonPart) == 0 || bytes.Equal(jsonPart, []byte("[DONE]")) {
		return line
	}
	choices := gjson.GetBytes(jsonPart, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return line
	}
	modified := false
	for idx, choice := range choices.Array() {
		content := choice.Get("delta.content")
		if !content.Exists() || !content.IsArray() {
			continue
		}
		var textParts []string
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				textParts = append(textParts, part.Get("text").String())
			}
		}
		path := "choices." + strconv.Itoa(idx) + ".delta.content"
		updated, err := sjson.SetBytes(jsonPart, path, strings.Join(textParts, ""))
		if err != nil {
			continue
		}
		jsonPart = updated
		modified = true
	}
	if !modified {
		return line
	}
	result := make([]byte, 0, len(prefix)+len(jsonPart))
	result = append(result, []byte(prefix)...)
	result = append(result, jsonPart...)
	return result
}

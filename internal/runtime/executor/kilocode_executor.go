package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

const (
	kilocodeBaseURL  = "https://kilo.ai/api/openrouter"
	kilocodeChatPath = "/chat/completions"
	kilocodeAuthType = "kilocode"
)

// KilocodeExecutor handles requests to the Kilocode API.
type KilocodeExecutor struct {
	cfg *config.Config
}

// normalizeKilocodeModelForAPI strips "kilocode-" prefix and normalizes model names for API calls.
// Examples:
//   - "kilocode-grok-code-fast-1" → "grok-code-fast-1"
//   - "kilocode-glm-4-7" → "glm-4.7"
//   - "kilocode-minimax-m2-1" → "minimax-m2.1"
func normalizeKilocodeModelForAPI(model string) string {
	// Strip "kilocode-" prefix
	normalized := strings.TrimPrefix(model, "kilocode-")

	// Convert version numbers from hyphens to dots
	// glm-4-7 → glm-4.7
	if strings.HasPrefix(normalized, "glm-4-") {
		normalized = strings.Replace(normalized, "glm-4-", "glm-4.", 1)
	}

	// minimax-m2-1 → minimax-m2.1
	if strings.HasPrefix(normalized, "minimax-m2-") {
		normalized = strings.Replace(normalized, "minimax-m2-", "minimax-m2.", 1)
	}

	return normalized
}

// NewKilocodeExecutor constructs a new executor instance.
func NewKilocodeExecutor(cfg *config.Config) *KilocodeExecutor {
	return &KilocodeExecutor{
		cfg: cfg,
	}
}

// Identifier implements ProviderExecutor.
func (e *KilocodeExecutor) Identifier() string { return kilocodeAuthType }

// PrepareRequest implements ProviderExecutor.
func (e *KilocodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}

	token := metaStringValue(auth.Metadata, "token")
	if token == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "missing kilocode token"}
	}

	e.applyHeaders(req, token)
	return nil
}

// HttpRequest injects Kilocode credentials into the request and executes it.
func (e *KilocodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kilocode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute handles non-streaming requests to Kilocode.
func (e *KilocodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token := metaStringValue(auth.Metadata, "token")
	if token == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "missing kilocode token"}
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	normalizedModel := normalizeKilocodeModelForAPI(req.Model)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, normalizedModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, normalizedModel, bytes.Clone(req.Payload), false)
	requestedModel := payloadRequestedModel(opts, normalizedModel)
	body = applyPayloadConfigWithRoot(e.cfg, normalizedModel, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.SetBytes(body, "stream", false)
	body, _ = sjson.SetBytes(body, "model", normalizedModel)

	url := kilocodeBaseURL + kilocodeChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	e.applyHeaders(httpReq, token)

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
		Body:      body,
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
			log.Errorf("kilocode executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if !isHTTPSuccess(httpResp.StatusCode) {
		data, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("kilocode executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	detail := parseOpenAIUsage(data)
	if detail.TotalTokens > 0 {
		reporter.publish(ctx, detail)
	}

	var param any
	converted := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(converted)}
	reporter.ensurePublished(ctx)
	return resp, nil
}

// ExecuteStream handles streaming requests to Kilocode.
func (e *KilocodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	token := metaStringValue(auth.Metadata, "token")
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing kilocode token"}
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	normalizedModel := normalizeKilocodeModelForAPI(req.Model)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, normalizedModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, normalizedModel, bytes.Clone(req.Payload), true)
	requestedModel := payloadRequestedModel(opts, normalizedModel)
	body = applyPayloadConfigWithRoot(e.cfg, normalizedModel, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.SetBytes(body, "model", normalizedModel)
	// Enable stream options for usage stats in stream
	body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)

	url := kilocodeBaseURL + kilocodeChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.applyHeaders(httpReq, token)

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
		Body:      body,
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

	if !isHTTPSuccess(httpResp.StatusCode) {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kilocode executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("kilocode executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out

	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("kilocode executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, maxScannerBufferSize)
		var param any

		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			// Parse SSE data
			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				if bytes.Equal(data, []byte("[DONE]")) {
					continue
				}
				if detail, ok := parseOpenAIStreamUsage(line); ok {
					reporter.publish(ctx, detail)
				}
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		} else {
			reporter.ensurePublished(ctx)
		}
	}()

	return stream, nil
}

// CountTokens is not supported for Kilocode.
func (e *KilocodeExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "count tokens not supported for kilocode"}
}

// Refresh validates the Kilocode token is still working.
// Kilocode API only supports /chat/completions endpoint, so we skip validation
// and return the auth as-is. Token validation will happen naturally during actual requests.
func (e *KilocodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}

	token := metaStringValue(auth.Metadata, "token")
	if token == "" {
		return auth, nil
	}

	// Kilocode API only supports /chat/completions, so we skip token validation here
	// Token validity will be checked during actual API requests
	return auth, nil
}

// applyHeaders sets the required headers for Kilocode API requests.
func (e *KilocodeExecutor) applyHeaders(r *http.Request, token string) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/json")
}

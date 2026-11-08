package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const deepAIProvider = "deepai"

// DeepAIExecutor forwards OpenAI-shaped requests to the deepai.org chat surface.
//
// Upstream is not an OpenAI endpoint: it takes a multipart form and answers with
// a plain-text stream carrying out-of-band packets, and anonymous callers mint a
// single-use tryit key per request whose hash is validated against the User-Agent.
type DeepAIExecutor struct {
	provider string
	cfg      *config.Config
}

// NewDeepAIExecutor creates a DeepAI executor instance.
func NewDeepAIExecutor(cfg *config.Config) *DeepAIExecutor {
	return &DeepAIExecutor{provider: deepAIProvider, cfg: cfg}
}

// Identifier returns the provider key handled by this executor.
func (e *DeepAIExecutor) Identifier() string { return e.provider }

func (e *DeepAIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("deepai executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	applyDeepAIHeaders(httpReq, deepAIStoredKey(auth))
	if cookie := deepAICookie(auth); cookie != "" {
		httpReq.Header.Set("Cookie", cookie)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming chat request.
func (e *DeepAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	upstreamModel := resolveDeepAIModelName(e.cfg, auth, baseModel)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	raw, headers, err := e.chatOnce(ctx, auth, translated, upstreamModel, opts.OriginalRequest)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}

	result := parseDeepAIStream(raw)
	text := result.text + deepAIImageMarkdown(result.images)
	body := deepAICompletionBody(baseModel, text, result.thinking, translated)
	reporter.Publish(ctx, deepAIEstimateUsage(translated, text, result.thinking))
	reporter.EnsurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: headers}, nil
}

// ExecuteStream performs a streaming chat request. Upstream always answers as a
// plain-text stream, so the settled body is split and re-emitted as OpenAI SSE.
func (e *DeepAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	upstreamModel := resolveDeepAIModelName(e.cfg, auth, baseModel)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetStream(true)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)

	httpResp, err := e.openChat(ctx, auth, translated, upstreamModel, opts.OriginalRequest)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(httpResp.Body, deepAIMaxErrorBody))
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("deepai executor: close response body error: %v", errClose)
		}
		err = deepAIStatusError(httpResp.StatusCode, b)
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("deepai executor: close response body error: %v", errClose)
			}
		}()

		var param any
		emit := func(payload map[string]any) bool {
			encoded, errMarshal := json.Marshal(payload)
			if errMarshal != nil {
				return true
			}
			line := append([]byte("data: "), encoded...)
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			for _, chunk := range sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, line, &param) {
				if !sendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Payload: chunk}) {
					return false
				}
			}
			return true
		}

		delta := func(content, reasoning string) bool {
			if content == "" && reasoning == "" {
				return true
			}
			payload := map[string]any{"role": "assistant"}
			if reasoning != "" {
				payload["reasoning_content"] = reasoning
			}
			if content != "" {
				payload["content"] = content
			}
			return emit(map[string]any{
				"id":      "chatcmpl-deepai",
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   baseModel,
				"choices": []map[string]any{{"index": 0, "delta": payload, "finish_reason": nil}},
			})
		}

		bodyErr := func(errStream error) {
			helps.RecordAPIResponseError(ctx, e.cfg, errStream)
			reporter.PublishFailure(ctx, errStream)
			sendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Err: errStream})
		}

		raw, errRead := readDeepAIStream(httpResp, nil)
		if errRead != nil {
			bodyErr(errRead)
			return
		}

		if refusal := deepAIJSONStatusRefusal([]byte(raw)); refusal != "" {
			bodyErr(deepAIStatusError(http.StatusOK, []byte(raw)))
			return
		}

		result := parseDeepAIStream(raw)
		// The upstream stream has no reliable token boundaries, so the answer is
		// emitted whole; reasoning precedes prose, images trail it.
		if !delta("", result.thinking) {
			return
		}
		if !delta(result.text, "") {
			return
		}
		if images := deepAIImageMarkdown(result.images); images != "" {
			if !delta(images, "") {
				return
			}
		}

		detail := deepAIEstimateUsage(translated, result.text, result.thinking)
		if !emit(map[string]any{
			"id":      "chatcmpl-deepai",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   baseModel,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage": map[string]any{
				"prompt_tokens":     detail.InputTokens,
				"completion_tokens": detail.OutputTokens,
				"total_tokens":      detail.TotalTokens,
			},
		}) {
			return
		}
		reporter.Publish(ctx, detail)
		reporter.EnsurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *DeepAIExecutor) chatOnce(ctx context.Context, auth *cliproxyauth.Auth, payload []byte, upstreamModel string, original []byte) (string, http.Header, error) {
	httpResp, err := e.openChat(ctx, auth, payload, upstreamModel, original)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("deepai executor: close response body error: %v", errClose)
		}
	}()

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(httpResp.Body, deepAIMaxErrorBody))
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		return "", nil, deepAIStatusError(httpResp.StatusCode, b)
	}

	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return "", nil, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	if taskID := deepAITaskID(body, httpResp.Header.Get("Content-Type")); taskID != "" {
		taskBody, errTask := e.waitForTask(ctx, auth, taskID)
		if errTask != nil {
			return "", nil, errTask
		}
		return taskBody, httpResp.Header.Clone(), nil
	}
	if refusal := deepAIJSONStatusRefusal(body); refusal != "" {
		return "", nil, deepAIStatusError(http.StatusOK, body)
	}
	return string(body), httpResp.Header.Clone(), nil
}

func (e *DeepAIExecutor) openChat(ctx context.Context, auth *cliproxyauth.Auth, payload []byte, upstreamModel string, original []byte) (*http.Response, error) {
	webAccess := deepAIWantsWebAccess(payload, original)
	body, contentType, _, errForm := deepAIBuildForm(payload, upstreamModel, webAccess)
	if errForm != nil {
		return nil, errForm
	}

	endpoint := deepAIBaseURL(auth) + deepAIChatPath
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header.Set("Content-Type", contentType)
	applyDeepAIHeaders(httpReq, deepAIStoredKey(auth))
	if cookie := deepAICookie(auth); cookie != "" {
		httpReq.Header.Set("Cookie", cookie)
	}
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
		URL:       endpoint,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// waitForTask polls the async reasoning endpoint until the task settles.
func (e *DeepAIExecutor) waitForTask(ctx context.Context, auth *cliproxyauth.Auth, taskID string) (string, error) {
	deadline := time.Now().Add(deepAITaskPollTimeout)
	query := url.Values{}
	query.Set("type", "thinking-task")
	query.Set("task_id", taskID)
	endpoint := deepAIBaseURL(auth) + deepAITaskStatusPath + "?" + query.Encode()

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	for time.Now().Before(deadline) {
		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if errReq != nil {
			return "", errReq
		}
		applyDeepAIHeaders(httpReq, deepAIStoredKey(auth))
		if cookie := deepAICookie(auth); cookie != "" {
			httpReq.Header.Set("Cookie", cookie)
		}
		resp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			return "", errDo
		}
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, deepAIMaxErrorBody))
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("deepai executor: close task body error: %v", errClose)
		}
		if errRead != nil {
			return "", errRead
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", deepAIStatusError(resp.StatusCode, body)
		}
		switch status := strings.ToUpper(strings.TrimSpace(gjson.GetBytes(body, "status").String())); status {
		case "COMPLETED", "COMPLETE", "SUCCESS":
			for _, path := range []string{"result", "text", "output", "response"} {
				if node := gjson.GetBytes(body, path); node.Type == gjson.String {
					return node.String(), nil
				}
			}
			return string(body), nil
		case "FAILED", "ERROR":
			return "", fmt.Errorf("deepai executor: upstream task failed: %s", gjson.GetBytes(body, "error").String())
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(deepAITaskPollInterval):
		}
	}
	return "", fmt.Errorf("deepai executor: upstream task timed out")
}

// Refresh is a no-op: DeepAI credentials do not expire.
func (e *DeepAIExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("deepai executor: refresh called")
	return auth, nil
}

// CountTokens is unsupported upstream.
func (e *DeepAIExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("deepai: count tokens not supported")
}

func deepAIBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if baseURL := strings.TrimSpace(auth.Attributes["base_url"]); baseURL != "" {
			return strings.TrimSuffix(baseURL, "/")
		}
	}
	return deepAIDefaultBaseURL
}

// deepAIStoredKey returns the configured dashboard key; the anonymous marker
// and an empty value both mean "mint a tryit key per request".
func deepAIStoredKey(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	key := strings.TrimSpace(auth.Attributes["api_key"])
	if strings.EqualFold(key, config.DeepAIAnonymousAPIKey) {
		return ""
	}
	return key
}

func deepAICookie(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["cookie"])
}

func resolveDeepAIModelName(cfg *config.Config, auth *cliproxyauth.Auth, model string) string {
	if cfg != nil && auth != nil {
		apiKey := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeAPIKey])
		for i := range cfg.DeepAIKey {
			entry := cfg.DeepAIKey[i]
			if apiKey != "" && !entry.ContainsAPIKey(apiKey) {
				continue
			}
			for _, m := range entry.Models {
				if strings.TrimSpace(m.Alias) == model && strings.TrimSpace(m.Name) != "" {
					return strings.TrimSpace(m.Name)
				}
			}
		}
	}
	return deepAIModelDefault(model)
}

// deepAIStatusError keeps a refusal's HTTP status so the conductor can cool the
// credential; upstream also reports refusals as JSON with HTTP 200.
func deepAIStatusError(status int, body []byte) error {
	message := strings.TrimSpace(string(body))
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if status == http.StatusOK {
		status = deepAIStatusFromRefusal(gjson.Get(message, "status").String())
	}
	return statusErr{code: status, msg: "deepai: " + message}
}

func deepAIStatusFromRefusal(reported string) int {
	lowered := strings.ToLower(strings.TrimSpace(reported))
	switch {
	case lowered == "":
		return http.StatusBadGateway
	case strings.Contains(lowered, "credit"), strings.Contains(lowered, "quota"), strings.Contains(lowered, "rate"), strings.Contains(lowered, "limit"):
		return http.StatusTooManyRequests
	case strings.Contains(lowered, "login"), strings.Contains(lowered, "sign"), strings.Contains(lowered, "auth"), strings.Contains(lowered, "key"):
		return http.StatusUnauthorized
	case strings.Contains(lowered, "pro"), strings.Contains(lowered, "upgrade"), strings.Contains(lowered, "plan"), strings.Contains(lowered, "paid"):
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}

func deepAIStatusCode(err error) int {
	if err == nil {
		return 0
	}
	if scoped, ok := err.(interface{ StatusCode() int }); ok {
		return scoped.StatusCode()
	}
	return http.StatusInternalServerError
}

// deepAIEstimateUsage approximates token counts because upstream reports none.
func deepAIEstimateUsage(payload []byte, text, thinking string) usage.Detail {
	input := int64(len(gjson.GetBytes(payload, "messages").Raw)/4) + 1
	output := int64((len(text)+len(thinking))/4) + 1
	return usage.Detail{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
}

func deepAICompletionBody(model, text, thinking string, payload []byte) []byte {
	message := map[string]any{"role": "assistant", "content": text}
	if thinking != "" {
		message["reasoning_content"] = thinking
	}
	detail := deepAIEstimateUsage(payload, text, thinking)
	body := map[string]any{
		"id":      "chatcmpl-deepai",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     detail.InputTokens,
			"completion_tokens": detail.OutputTokens,
			"total_tokens":      detail.TotalTokens,
		},
	}
	encoded, errEncode := json.Marshal(body)
	if errEncode != nil {
		return []byte(`{"object":"chat.completion","choices":[]}`)
	}
	return encoded
}

func readDeepAIStream(resp *http.Response, onRead func(accumulated string) bool) (string, error) {
	var accumulated bytes.Buffer
	buffer := make([]byte, 4096)
	for {
		n, errRead := resp.Body.Read(buffer)
		if n > 0 {
			accumulated.Write(buffer[:n])
			if onRead != nil && !onRead(accumulated.String()) {
				return accumulated.String(), nil
			}
		}
		if errRead != nil {
			if errRead == io.EOF {
				return accumulated.String(), nil
			}
			return accumulated.String(), errRead
		}
	}
}

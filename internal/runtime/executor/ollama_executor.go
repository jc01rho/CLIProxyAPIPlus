package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const ollamaDefaultBaseURL = "https://ollama.com/api"

type OllamaExecutor struct {
	cfg *config.Config
}

func NewOllamaExecutor(cfg *config.Config) *OllamaExecutor { return &OllamaExecutor{cfg: cfg} }

func (e *OllamaExecutor) Identifier() string { return "ollama" }

func (e *OllamaExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := ollamaCredentials(auth)
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

func (e *OllamaExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("ollama executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return newProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

func resolveOllamaExecutionModel(auth *cliproxyauth.Auth, model string) string {
	model = strings.TrimSpace(model)
	if model == "" || auth == nil || strings.TrimSpace(auth.ID) == "" {
		return model
	}

	models := registry.GetGlobalRegistry().GetModelsForClient(auth.ID)
	for _, info := range models {
		if info == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(info.ID), model) {
			continue
		}
		if target := strings.TrimSpace(info.ExecutionTarget); target != "" {
			return target
		}
		break
	}

	return model
}

func (e *OllamaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := resolveOllamaExecutionModel(auth, thinking.ParseSuffix(req.Model).ModelName)
	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	apiKey, baseURL := ollamaCredentials(auth)
	if apiKey == "" {
		return resp, fmt.Errorf("ollama executor: missing api key")
	}
	if baseURL == "" {
		baseURL = ollamaDefaultBaseURL
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, false)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)
	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel, requestPath)
	ollamaPayload := buildOllamaChatPayload(translated, baseModel, false)

	url := strings.TrimSuffix(baseURL, "/") + "/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(ollamaPayload))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("User-Agent", "cli-proxy-ollama")
	var attrs map[string]string
	var authID, authLabel, authType, authValue string
	if auth != nil {
		attrs = auth.Attributes
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: ollamaPayload, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})

	httpResp, err := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("ollama executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		logDetailedAPIError(ctx, e.cfg, e.Identifier(), baseModel, url, httpResp.StatusCode, httpResp.Header.Get("Content-Type"), ollamaPayload, body)
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return resp, err
	}
	openAIResponse := ollamaChatToOpenAI(body, baseModel)
	reporter.publish(ctx, parseOpenAIUsage(openAIResponse))
	reporter.ensurePublished(ctx)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, openAIResponse, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OllamaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := resolveOllamaExecutionModel(auth, thinking.ParseSuffix(req.Model).ModelName)
	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	apiKey, baseURL := ollamaCredentials(auth)
	if apiKey == "" {
		return nil, fmt.Errorf("ollama executor: missing api key")
	}
	if baseURL == "" {
		baseURL = ollamaDefaultBaseURL
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, false)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)
	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel, requestPath)
	ollamaPayload := buildOllamaChatPayload(translated, baseModel, true)

	url := strings.TrimSuffix(baseURL, "/") + "/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(ollamaPayload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("User-Agent", "cli-proxy-ollama")
	var attrs map[string]string
	var authID, authLabel, authType, authValue string
	if auth != nil {
		attrs = auth.Attributes
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: ollamaPayload, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})

	httpResp, err := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logDetailedAPIError(ctx, e.cfg, e.Identifier(), baseModel, url, httpResp.StatusCode, httpResp.Header.Get("Content-Type"), ollamaPayload, b)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("ollama executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("ollama executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}
			done := gjson.GetBytes(trimmed, "done").Bool()
			sseData := ollamaStreamChunkToOpenAI(trimmed, baseModel, done)
			if done {
				// Extract usage from the final chunk and publish
				openAIFinal := ollamaChatToOpenAI(trimmed, baseModel)
				reporter.publish(ctx, parseOpenAIUsage(openAIFinal))
				// Send the done chunk through the translator then the [DONE] signal
				chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, sseData, &param)
				for i := range chunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
					case <-ctx.Done():
						return
					}
				}
				doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
				for i := range doneChunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
					case <-ctx.Done():
						return
					}
				}
				reporter.ensurePublished(ctx)
				return
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, sseData, &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else {
			// Stream ended without a done:true chunk — emit synthetic [DONE]
			doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range doneChunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
				case <-ctx.Done():
					return
				}
			}
			reporter.ensurePublished(ctx)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// ollamaStreamChunkToOpenAI converts a single Ollama NDJSON streaming chunk to an OpenAI SSE data line.
func ollamaStreamChunkToOpenAI(chunk []byte, model string, done bool) []byte {
	content := gjson.GetBytes(chunk, "message.content").String()
	thinking := gjson.GetBytes(chunk, "message.thinking").String()
	created := time.Now().Unix()

	delta := map[string]any{"role": "assistant", "content": content}
	if thinking != "" {
		delta["thinking"] = thinking
	}

	var finishReason any
	if done {
		finishReason = "stop"
	}

	openAIChunk := map[string]any{
		"id":      "chatcmpl-ollama",
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finishReason}},
	}
	chunkJSON, _ := json.Marshal(openAIChunk)
	return append([]byte("data: "), chunkJSON...)
}

func (e *OllamaExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = auth
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)
	enc, err := tokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("ollama executor: tokenizer init failed: %w", err)
	}
	count, err := countOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("ollama executor: token counting failed: %w", err)
	}
	usageJSON := buildOpenAIUsageJSON(count)
	return cliproxyexecutor.Response{Payload: sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)}, nil
}

func (e *OllamaExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	_ = ctx
	return auth, nil
}

func FetchOllamaModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	apiKey, baseURL := ollamaCredentials(auth)
	if apiKey == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = ollamaDefaultBaseURL
	}
	// Ollama Cloud: try /v1/tags first (returns OpenAI-compatible format), fallback to /api/tags
	endpointPaths := []string{"/v1/tags", "/api/tags"}
	if !strings.Contains(strings.ToLower(baseURL), "ollama.com") {
		// Self-hosted Ollama: try /api/tags first, fallback to /tags
		endpointPaths = []string{"/api/tags", "/tags"}
	}
	var lastURL string
	var lastStatus int
	var lastBody []byte
	for _, path := range endpointPaths {
		url := strings.TrimSuffix(baseURL, "/") + path
		lastURL = url
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			log.Errorf("ollama fetch models: create request error: %v", err)
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("User-Agent", "cli-proxy-ollama")
		if auth != nil {
			util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
		}
		var authID, authLabel, authType, authValue string
		if auth != nil {
			authID = auth.ID
			authLabel = auth.Label
			authType, authValue = auth.AccountInfo()
		}
		recordAPIRequest(ctx, cfg, upstreamRequestLog{URL: url, Method: http.MethodGet, Headers: req.Header.Clone(), Body: nil, Provider: "ollama", AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})
		resp, err := newProxyAwareHTTPClient(ctx, cfg, auth, 15*time.Second).Do(req)
		if err != nil {
			recordAPIResponseError(ctx, cfg, err)
			log.Errorf("ollama models fetch failed for %s: %v", url, err)
			continue
		}
		defer resp.Body.Close()
		body, readErr := io.ReadAll(resp.Body)
		recordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
		lastStatus = resp.StatusCode
		lastBody = body
		if readErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			appendAPIResponseChunk(ctx, cfg, body)
			return parseOllamaTags(body)
		}
		// try next endpoint
	}
	if lastStatus != 0 {
		logDetailedAPIError(ctx, cfg, "ollama", "", lastURL, lastStatus, "application/json", nil, lastBody)
		log.Errorf("ollama models fetch error status: %d url: %s body: %s", lastStatus, lastURL, string(lastBody))
	}
	return nil
}

func ollamaCredentials(auth *cliproxyauth.Auth) (apiKey, baseURL string) {
	if auth == nil || auth.Attributes == nil {
		return "", ""
	}
	apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	return apiKey, baseURL
}

func buildOllamaChatPayload(openAIPayload []byte, model string, stream bool) []byte {
	var src map[string]any
	if err := json.Unmarshal(openAIPayload, &src); err != nil {
		return buildBasicOllamaPayload(model, stream)
	}

	payload := map[string]any{"model": model, "stream": stream}

	if messages, ok := src["messages"].([]any); ok {
		payload["messages"] = convertOpenAIMessagesToOllama(messages)
	} else {
		payload["messages"] = []any{}
	}

	if temp, ok := src["temperature"]; ok {
		payload["temperature"] = temp
	}
	if topP, ok := src["top_p"]; ok {
		payload["top_p"] = topP
	}

	out, _ := json.Marshal(payload)
	return out
}

// convertOpenAIMessagesToOllama converts OpenAI-format messages to Ollama-compatible format.
// Ollama /api/chat only supports role (system/user/assistant) and content (string).
// Role "tool" messages, tool_calls, tool_call_id, and other unsupported fields are stripped.
func convertOpenAIMessagesToOllama(openAIMessages []any) []map[string]any {
	ollama := make([]map[string]any, 0, len(openAIMessages))
	for _, msg := range openAIMessages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		// Skip tool messages — Ollama doesn't support function calling
		if role == "tool" {
			continue
		}
		m := map[string]any{"role": role}
		if content, ok := msgMap["content"]; ok && content != nil {
			m["content"] = convertOpenAIContentToString(content)
		} else {
			m["content"] = ""
		}
		ollama = append(ollama, m)
	}
	return ollama
}

func convertOpenAIContentToString(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, item := range c {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t, _ := itemMap["type"].(string)
			if t == "text" || t == "output_text" {
				if text, ok := itemMap["text"].(string); ok {
					sb.WriteString(text)
				}
			}
		}
		return sb.String()
	default:
		return fmt.Sprintf("%v", content)
	}
}

func buildBasicOllamaPayload(model string, stream bool) []byte {
	payload := map[string]any{"model": model, "messages": []any{}, "stream": stream}
	out, _ := json.Marshal(payload)
	return out
}

func ollamaChatToOpenAI(body []byte, model string) []byte {
	content := gjson.GetBytes(body, "message.content").String()
	created := time.Now().Unix()
	resp := map[string]any{
		"id":      "chatcmpl-ollama",
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
	}
	promptTokens := gjson.GetBytes(body, "prompt_eval_count").Int()
	completionTokens := gjson.GetBytes(body, "eval_count").Int()
	if promptTokens > 0 || completionTokens > 0 {
		resp["usage"] = map[string]any{"prompt_tokens": promptTokens, "completion_tokens": completionTokens, "total_tokens": promptTokens + completionTokens}
	}
	out, _ := json.Marshal(resp)
	return out
}

func parseOllamaTags(body []byte) []*registry.ModelInfo {
	models := gjson.GetBytes(body, "models")
	if !models.IsArray() {
		return nil
	}
	now := time.Now().Unix()
	out := make([]*registry.ModelInfo, 0)
	seen := map[string]struct{}{}
	models.ForEach(func(_, item gjson.Result) bool {
		name := strings.TrimSpace(item.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(item.Get("model").String())
		}
		if name == "" {
			return true
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return true
		}
		seen[key] = struct{}{}
		out = append(out, &registry.ModelInfo{ID: name, Object: "model", Created: now, OwnedBy: "ollama", Type: "ollama", DisplayName: name})
		return true
	})
	return out
}

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
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

func (e *OllamaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
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

func (e *OllamaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_ = ctx
	_ = auth
	_ = req
	_ = opts
	return nil, statusErr{code: http.StatusNotImplemented, msg: "ollama streaming is not supported"}
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
	url := strings.TrimSuffix(baseURL, "/") + "/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "cli-proxy-ollama")
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	resp, err := newProxyAwareHTTPClient(ctx, cfg, auth, 15*time.Second).Do(req)
	if err != nil {
		log.Debugf("ollama models fetch failed: %v", err)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	return parseOllamaTags(body)
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
	messageRaw := json.RawMessage("[]")
	if raw := gjson.GetBytes(openAIPayload, "messages").Raw; raw != "" {
		messageRaw = json.RawMessage(raw)
	}
	payload := map[string]any{"model": model, "messages": messageRaw, "stream": stream}
	if v := gjson.GetBytes(openAIPayload, "temperature"); v.Exists() {
		payload["temperature"] = v.Value()
	}
	if v := gjson.GetBytes(openAIPayload, "top_p"); v.Exists() {
		payload["top_p"] = v.Value()
	}
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

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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	commandCodeBaseURL = "https://api.commandcode.ai"
	commandCodeVersion = "1.6.0"
	commandCodeProject = "cli-proxy"
)

// CommandCodeExecutor handles requests to CommandCode API (/alpha/generate).
type CommandCodeExecutor struct {
	provider string
	cfg      *config.Config
}

// NewCommandCodeExecutor creates a new CommandCode executor instance.
func NewCommandCodeExecutor(cfg *config.Config) *CommandCodeExecutor {
	return &CommandCodeExecutor{provider: "commandcode", cfg: cfg}
}

// Identifier returns the provider key handled by this executor.
func (e *CommandCodeExecutor) Identifier() string { return e.provider }

// HttpRequest injects CommandCode credentials and executes the request.
func (e *CommandCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("commandcode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	apiKey := commandCodeAPIKey(auth)
	applyCommandCodeHeaders(httpReq, apiKey)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute handles non-streaming execution against the CommandCode API.
func (e *CommandCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	baseModel = resolveCommandCodeModelName(e.cfg, auth, baseModel)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	apiKey := commandCodeAPIKey(auth)
	if apiKey == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "commandcode: missing API key"}
		return
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	payload, err := buildCommandCodePayload(translated, baseModel, true)
	if err != nil {
		return resp, fmt.Errorf("commandcode: build payload: %w", err)
	}

	url := commandCodeGenerateURL(auth)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}
	applyCommandCodeHeaders(httpReq, apiKey)
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
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		Model:     baseModel,
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
			log.Errorf("commandcode executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	// Collect NDJSON events into the shared stream accumulator.
	acc := newCommandCodeStreamAccumulator()
	var rawLines [][]byte

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800)
	for scanner.Scan() {
		line := scanner.Bytes()
		appendAPIResponseChunk(ctx, e.cfg, line)
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		rawLines = append(rawLines, append([]byte(nil), trimmed...))
		acc.feed(decodeCommandCodeStreamEvent(trimmed))
		if acc.terminal != commandCodeTerminalNone {
			break
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		recordAPIResponseError(ctx, e.cfg, errScan)
		return resp, errScan
	}
	if acc.err != nil {
		recordAPIResponseError(ctx, e.cfg, acc.err)
		err = acc.err
		return resp, err
	}

	// Legacy fallback: a body without any recognized NDJSON event may be a
	// single JSON envelope. Recover its text before declaring truncation.
	if !acc.sawKnownEvent {
		if fallbackText := commandCodeRecoverFromRawBody(rawLines); fallbackText != "" {
			acc.adoptLegacyText(fallbackText)
		}
	}
	if errEOF := acc.finishEOF(); errEOF != nil {
		recordAPIResponseError(ctx, e.cfg, errEOF)
		err = errEOF
		return resp, err
	}

	// Build an OpenAI-shaped response to feed through the translator.
	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	message := map[string]any{
		"role":    "assistant",
		"content": acc.text.String(),
	}
	if toolCalls := acc.openAIToolCalls(); len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	openAIResp := map[string]any{
		"id":      chatID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   baseModel,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": acc.stopReason,
			},
		},
		"usage": commandCodeOpenAIUsage(acc.usage()),
	}
	body, err := json.Marshal(openAIResp)
	if err != nil {
		return resp, fmt.Errorf("commandcode: marshal response: %w", err)
	}

	reporter.publish(ctx, parseOpenAIUsage(body))
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream handles streaming execution against the CommandCode API.
func (e *CommandCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	baseModel = resolveCommandCodeModelName(e.cfg, auth, baseModel)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	apiKey := commandCodeAPIKey(auth)
	if apiKey == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "commandcode: missing API key"}
		return nil, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)

	payload, err := buildCommandCodePayload(translated, baseModel, true)
	if err != nil {
		return nil, fmt.Errorf("commandcode: build payload: %w", err)
	}

	url := commandCodeGenerateURL(auth)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	applyCommandCodeHeaders(httpReq, apiKey)
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
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		Model:     baseModel,
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
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("commandcode executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("commandcode executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		toolCallIndex := 0
		acc := newCommandCodeStreamAccumulator()

		sendSSELine := func(sseLine []byte) {
			appendAPIResponseChunk(ctx, e.cfg, sseLine)
			if detail, ok := parseOpenAIStreamUsage(sseLine); ok {
				reporter.publish(ctx, detail)
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(sseLine), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
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

		for scanner.Scan() {
			line := scanner.Bytes()
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}

			event := decodeCommandCodeStreamEvent(trimmed)
			acc.feed(event)
			switch event.kind {
			case commandCodeEventTextDelta, commandCodeEventToolResult:
				if event.text != "" {
					emitChunk(commandCodeStreamChunk(chatID, baseModel, map[string]any{
						"role":    "assistant",
						"content": event.text,
					}, ""))
				}

			case commandCodeEventToolCall:
				emitChunk(commandCodeStreamChunk(chatID, baseModel, map[string]any{
					"tool_calls": []map[string]any{
						{
							"index": toolCallIndex,
							"id":    event.toolCallID,
							"type":  "function",
							"function": map[string]any{
								"name":      event.toolName,
								"arguments": string(event.toolInput),
							},
						},
					},
				}, ""))
				toolCallIndex++

			case commandCodeEventFinish, commandCodeEventAbort:
				emitChunk(map[string]any{
					"id":      chatID,
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   baseModel,
					"choices": []map[string]any{
						{
							"index":         0,
							"delta":         map[string]any{},
							"finish_reason": acc.stopReason,
						},
					},
					"usage": commandCodeOpenAIUsage(acc.usage()),
				})
				sendSSELine([]byte("data: [DONE]"))
			}
			if acc.terminal != commandCodeTerminalNone {
				break
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}
		streamErr := acc.err
		if streamErr == nil {
			streamErr = acc.finishEOF()
		}
		if streamErr != nil {
			recordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.publishFailure(ctx)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// Refresh is a no-op for API-key based auth.
func (e *CommandCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("commandcode executor: refresh called")
	return auth, nil
}

// CountTokens is not supported by the CommandCode API.
func (e *CommandCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("commandcode: count tokens not supported")
}

// commandCodeAPIKey extracts the API key from auth attributes.
func commandCodeAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		for _, keyName := range []string{"api_key", "apiKey", "key", "commandcode", "access"} {
			if key := strings.TrimSpace(auth.Attributes[keyName]); key != "" {
				return key
			}
		}
	}
	return ""
}

func commandCodeGenerateURL(auth *cliproxyauth.Auth) string {
	baseURL := commandCodeBaseURL
	if auth != nil && auth.Attributes != nil {
		if configured := strings.TrimSpace(auth.Attributes["base_url"]); configured != "" {
			baseURL = configured
		}
	}
	return strings.TrimRight(baseURL, "/") + "/alpha/generate"
}

// applyCommandCodeHeaders sets the required CommandCode request headers.
func applyCommandCodeHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("x-command-code-version", commandCodeVersion)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-project-slug", commandCodeProject)
	req.Header.Set("x-taste-learning", "true")
	req.Header.Set("x-co-flag", "false")
}

// resolveCommandCodeModelName resolves a model alias to the actual upstream model name
// by looking up the CommandCodeKey configuration. If the model is not an alias, it returns
// the model unchanged.
func resolveCommandCodeModelName(cfg *config.Config, auth *cliproxyauth.Auth, model string) string {
	if cfg == nil || auth == nil {
		return model
	}
	apiKey := commandCodeAPIKey(auth)
	for _, key := range cfg.CommandCodeKey {
		if key.APIKey != apiKey {
			continue
		}
		for _, m := range key.Models {
			if m.Alias == model {
				return m.Name
			}
		}
	}
	return model
}

func compactCommandCodeTextParts(parts []string) []string {
	filtered := parts[:0]
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return filtered
}

func commandCodeStreamChunk(id, model string, delta map[string]any, finishReason string) map[string]any {
	choice := map[string]any{
		"index":         0,
		"delta":         delta,
		"finish_reason": nil,
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{choice},
	}
}

// commandCodeFallbackText extracts text from an unrecognized NDJSON event
// line. It probes common text-bearing field paths so that upstream format
// drift (renamed fields, new event types) does not silently drop content.
func commandCodeFallbackText(line []byte) string {
	for _, path := range []string{
		"text",
		"delta.text",
		"delta.content",
		"content",
		"message.content",
		"output_text",
		"outputText",
	} {
		if v := gjson.GetBytes(line, path); v.Exists() && v.Type == gjson.String {
			if s := v.String(); s != "" {
				return s
			}
		}
	}
	return ""
}

// commandCodeRecoverFromRawBody attempts to recover assistant text when the
// structured NDJSON parser produced nothing. It handles two legacy shapes:
// a single JSON envelope (whole body is one object) and a body where the
// text lives under choices/message/content paths.
func commandCodeRecoverFromRawBody(lines [][]byte) string {
	if len(lines) == 0 {
		return ""
	}

	candidates := make([][]byte, 0, len(lines)+1)
	candidates = append(candidates, bytes.Join(lines, []byte("\n")))
	if len(lines) > 1 {
		candidates = append(candidates, lines...)
	}

	for _, candidate := range candidates {
		// Hidden reasoning must never be surfaced by the generic fallback.
		if gjson.GetBytes(candidate, "type").String() == "reasoning-delta" {
			continue
		}
		for _, path := range []string{
			"choices.0.message.content",
			"message.content",
			"content",
			"result",
			"output",
			"text",
		} {
			if v := gjson.GetBytes(candidate, path); v.Exists() {
				switch v.Type {
				case gjson.String:
					if s := v.String(); s != "" {
						return s
					}
				case gjson.JSON:
					// content may be an array of parts; flatten text blocks.
					if flattened := commandCodeFallbackFlattenContent(v); flattened != "" {
						return flattened
					}
				}
			}
		}
	}
	return ""
}

// commandCodeFallbackFlattenContent collapses a JSON content value (string,
// array of typed parts, or object) into plain text.
func commandCodeFallbackFlattenContent(v gjson.Result) string {
	switch v.Type {
	case gjson.String:
		return v.String()
	case gjson.JSON:
		if v.IsArray() {
			var parts []string
			for _, item := range v.Array() {
				if t := item.Get("text"); t.Exists() && t.Type == gjson.String {
					if s := t.String(); s != "" {
						parts = append(parts, s)
					}
				}
			}
			return strings.Join(compactCommandCodeTextParts(parts), "\n")
		}
		if t := v.Get("text"); t.Exists() && t.Type == gjson.String {
			return t.String()
		}
	}
	return ""
}

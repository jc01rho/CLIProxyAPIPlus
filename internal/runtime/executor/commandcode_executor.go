package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	commandCodeVersion = "0.26.20"
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

	url := commandCodeBaseURL + "/alpha/generate"
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

	// Collect text-delta events into a single response body.
	var textContent strings.Builder
	var inputTokens, outputTokens int64
	finishReason := "stop"

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800)
	for scanner.Scan() {
		line := scanner.Bytes()
		appendAPIResponseChunk(ctx, e.cfg, line)
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		switch gjson.GetBytes(trimmed, "type").String() {
		case "text-delta":
			textContent.WriteString(gjson.GetBytes(trimmed, "text").String())
		case "finish":
			inputTokens = gjson.GetBytes(trimmed, "totalUsage.inputTokens").Int()
			outputTokens = gjson.GetBytes(trimmed, "totalUsage.outputTokens").Int()
			if fr := gjson.GetBytes(trimmed, "finishReason").String(); fr != "" {
				finishReason = fr
			}
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		recordAPIResponseError(ctx, e.cfg, errScan)
		return resp, errScan
	}

	// Build an OpenAI-shaped response to feed through the translator.
	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	openAIResp := map[string]any{
		"id":      chatID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   baseModel,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": textContent.String(),
				},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
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

	url := commandCodeBaseURL + "/alpha/generate"
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

		for scanner.Scan() {
			line := scanner.Bytes()
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}

			eventType := gjson.GetBytes(trimmed, "type").String()
			switch eventType {
			case "text-delta":
				text := gjson.GetBytes(trimmed, "text").String()
				chunk := commandCodeStreamChunk(chatID, baseModel, map[string]any{
					"role":    "assistant",
					"content": text,
				}, "")
				b, errMarshal := json.Marshal(chunk)
				if errMarshal != nil {
					continue
				}
				sendSSELine(append([]byte("data: "), b...))

			case "reasoning-delta":
				// Skip reasoning deltas; not surfaced to downstream clients.

			case "tool-call":
				toolCallID := gjson.GetBytes(trimmed, "toolCallId").String()
				toolName := gjson.GetBytes(trimmed, "toolName").String()
				inputRaw := gjson.GetBytes(trimmed, "input").Raw
				if inputRaw == "" {
					inputRaw = "{}"
				}
				chunk := commandCodeStreamChunk(chatID, baseModel, map[string]any{
					"tool_calls": []map[string]any{
						{
							"index": toolCallIndex,
							"id":    toolCallID,
							"type":  "function",
							"function": map[string]any{
								"name":      toolName,
								"arguments": inputRaw,
							},
						},
					},
				}, "")
				toolCallIndex++
				b, errMarshal := json.Marshal(chunk)
				if errMarshal != nil {
					continue
				}
				sendSSELine(append([]byte("data: "), b...))

			case "finish":
				inputTokens := gjson.GetBytes(trimmed, "totalUsage.inputTokens").Int()
				outputTokens := gjson.GetBytes(trimmed, "totalUsage.outputTokens").Int()
				fr := gjson.GetBytes(trimmed, "finishReason").String()
				if fr == "" {
					fr = "stop"
				}
				finishChunk := map[string]any{
					"id":      chatID,
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   baseModel,
					"choices": []map[string]any{
						{
							"index":         0,
							"delta":         map[string]any{},
							"finish_reason": fr,
						},
					},
					"usage": map[string]any{
						"prompt_tokens":     inputTokens,
						"completion_tokens": outputTokens,
						"total_tokens":      inputTokens + outputTokens,
					},
				}
				b, errMarshal := json.Marshal(finishChunk)
				if errMarshal == nil {
					sendSSELine(append([]byte("data: "), b...))
				}
				sendSSELine([]byte("data: [DONE]"))
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
		if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
			return key
		}
	}
	return ""
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
	req.Header.Set("x-taste-learning", "false")
	req.Header.Set("x-co-flag", "false")
	req.Header.Set("x-session-id", generateCommandCodeSessionID())
}

// buildCommandCodePayload constructs the CommandCode envelope from an OpenAI-format payload.
// It extracts system/developer messages into the top-level system field, converts tools and
// tool-related messages to CommandCode format, and removes system messages from the messages array.
func buildCommandCodePayload(openAIPayload []byte, model string, stream bool) ([]byte, error) {
	var systemContent string
	var filteredMessages []json.RawMessage

	messagesRaw := gjson.GetBytes(openAIPayload, "messages")
	if messagesRaw.Exists() && messagesRaw.IsArray() {
		for _, msg := range messagesRaw.Array() {
			role := msg.Get("role").String()
			if role == "system" || role == "developer" {
				content := msg.Get("content").String()
				if systemContent == "" {
					systemContent = content
				} else {
					systemContent += "\n" + content
				}
				continue
			}
			filteredMessages = append(filteredMessages, json.RawMessage(msg.Raw))
		}
	}
	if filteredMessages == nil {
		filteredMessages = []json.RawMessage{}
	}

	if model == "" {
		model = gjson.GetBytes(openAIPayload, "model").String()
	}

	maxTokens := gjson.GetBytes(openAIPayload, "max_tokens").Int()
	if maxTokens == 0 {
		maxTokens = 16384
	}

	// Convert tools from OpenAI format to CommandCode format.
	var convertedTools []json.RawMessage
	toolsRaw := gjson.GetBytes(openAIPayload, "tools")
	if toolsRaw.Exists() && toolsRaw.IsArray() {
		for _, tool := range toolsRaw.Array() {
			funcObj := tool.Get("function")
			name := funcObj.Get("name").String()
			description := funcObj.Get("description").String()
			parameters := funcObj.Get("parameters").Raw
			if parameters == "" || parameters == "null" {
				parameters = `{"type":"object","properties":{}}`
			}
			ccTool := map[string]any{
				"type":        "function",
				"name":        name,
				"description": description,
				"input_schema": json.RawMessage(parameters),
			}
			b, _ := json.Marshal(ccTool)
			convertedTools = append(convertedTools, json.RawMessage(b))
		}
	}

	params := map[string]any{
		"model":      model,
		"messages":   filteredMessages,
		"max_tokens": maxTokens,
		"stream":     stream,
	}
	if systemContent != "" {
		params["system"] = systemContent
	}
	if len(convertedTools) > 0 {
		params["tools"] = convertedTools
	}
	if temp := gjson.GetBytes(openAIPayload, "temperature"); temp.Exists() {
		params["temperature"] = temp.Float()
	}

	now := time.Now()
	envelope := map[string]any{
		"config": map[string]any{
			"workingDir":    "/tmp",
			"date":          now.Format("2006-01-02"),
			"environment":   "terminal",
			"structure":     []any{},
			"isGitRepo":     false,
			"currentBranch": "",
			"mainBranch":    "",
			"gitStatus":     "",
			"recentCommits": []any{},
		},
		"memory":         "",
		"taste":          "",
		"skills":         nil,
		"permissionMode": "standard",
		"params":         params,
	}
	return json.Marshal(envelope)
}

// generateCommandCodeSessionID creates a random session ID for x-session-id header.
func generateCommandCodeSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("cc-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
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

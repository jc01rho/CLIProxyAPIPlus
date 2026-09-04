package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	commandCodeVersion = "1.12.0"
	// commandCodeUserAgent mirrors the official CLI's literal User-Agent value.
	commandCodeUserAgent = "cli"
	// commandCodeProject matches the workspace slug shape the CLI derives from
	// its current project directory (a bounded lowercase slug).
	commandCodeProject = "workspace"
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
	applyCommandCodeHeaders(httpReq, apiKey, newCommandCodeSessionID())
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

	// Run the pause_turn continuation chain: the installed command-code@1.12.0
	// client re-posts on a pause_turn finish reason up to a bounded number of
	// times; the proxy mirrors that so long generations are not truncated.
	// Request construction, header application, and request logging happen
	// inside commandCodePauseChain/commandCodePostOne so every continuation
	// attempt is captured with the same field shape.

	acc, _, err := commandCodePauseChain(ctx, e.cfg, auth, e.Identifier(), payload, apiKey)
	if err != nil {
		return resp, err
	}
	// Build an OpenAI-shaped response to feed through the translator.
	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	message := map[string]any{
		"role":    "assistant",
		"content": acc.text.String(),
	}
	if reasoning := acc.reasoning.String(); reasoning != "" {
		message["reasoning_content"] = reasoning
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
	resp = cliproxyexecutor.Response{Payload: out, Headers: nil}
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
	// Streaming pause_turn continuation chain: the installed command-code@1.12.0
	// client re-posts on a pause_turn finish reason. The goroutine below calls
	// commandCodeStreamOneAttempt (which owns its own request lifecycle and
	// logs) and loops while IsPauseTurn() is true.
	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	out := make(chan cliproxyexecutor.StreamChunk)

	attemptCtx := commandCodeAttemptContext{
		ctx:         ctx,
		cfg:         e.cfg,
		auth:        auth,
		provider:    e.Identifier(),
		baseModel:   baseModel,
		from:        from,
		to:          to,
		openAIInput: translated,
		wirePayload: payload,
		original:    req.Payload,
		out:         out,
		reporter:    reporter,
		chatID:      chatID,
		sessionID:   newCommandCodeSessionID(),
	}

	go func() {
		defer close(out)
		master := newCommandCodeStreamAccumulator()
		toolCallOffset := 0
		for attempt := 0; ; attempt++ {
			attemptCtx.toolCallOffset = toolCallOffset
			acc, attemptErr := commandCodeStreamOneAttempt(ctx, e.cfg, auth, e.Identifier(), baseModel, apiKey, attemptCtx)
			if acc != nil {
				toolCallOffset += len(acc.toolCalls)
				master.merge(acc)
			}
			if attemptErr != nil {
				return
			}
			if !acc.IsPauseTurn() || attempt >= commandCodeMaxContinuations {
				reporter.ensurePublished(ctx)
				return
			}
			if err := ctx.Err(); err != nil {
				return
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: nil,
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
	return commandCodeBaseURLForAuth(auth) + "/alpha/generate"
}

// applyCommandCodeHeaders sets the required CommandCode request headers. The
// sessionID parameter is forwarded into the x-session-id header (matches the
// installed command-code@1.12.0 client format sess_<16hex>) so the upstream
// API can associate pause_turn continuation posts with the originating
// session. Pass an empty string to omit the header.
func applyCommandCodeHeaders(req *http.Request, apiKey string, sessionID string) {
	req.Header.Set("Content-Type", "application/json")
	// The official CLI identifies itself with a literal "cli" User-Agent; the
	// upstream rejects requests that do not look like the CLI with
	// "Proxy use detected" (400), so mirror the CLI's wire identity.
	req.Header.Set("User-Agent", commandCodeUserAgent)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if sessionID != "" {
		req.Header.Set("x-session-id", sessionID)
	}
	req.Header.Set("x-command-code-version", commandCodeVersion)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-project-slug", commandCodeProject)
	req.Header.Set("x-taste-learning", "false")
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
		if !key.MatchesCredential(apiKey, auth.ProxyURL) {
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

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
	"sync"
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
	mimoCodeDefaultBaseURL = "https://api.xiaomimimo.com"
	mimoCodeBootstrapPath  = "/api/free-ai/bootstrap"
	mimoCodeChatPath       = "/api/free-ai/openai/chat"
	mimoCodeSourceHeader   = "mimocode-cli"
)

// MiMoCodeExecutor handles requests to MiMo Code API with JWT bootstrap authentication.
type MiMoCodeExecutor struct {
	provider     string
	cfg          *config.Config
	jwtCache     sync.Map // map[string]cachedJWT
}

type cachedJWT struct {
	token     string
	expiresAt time.Time
}

// NewMiMoCodeExecutor creates a new MiMo Code executor instance.
func NewMiMoCodeExecutor(cfg *config.Config) *MiMoCodeExecutor {
	return &MiMoCodeExecutor{provider: "mimo-code", cfg: cfg}
}

// Identifier returns the provider key handled by this executor.
func (e *MiMoCodeExecutor) Identifier() string { return e.provider }

// HttpRequest injects MiMo Code credentials and executes the request.
func (e *MiMoCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("mimo-code executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	apiKey := mimoCodeAPIKey(auth)
	applyMiMoCodeHeaders(httpReq, apiKey)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute handles non-streaming execution against the MiMo Code API.
func (e *MiMoCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	baseModel = resolveMiMoCodeModelName(e.cfg, auth, baseModel)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	apiKey := mimoCodeAPIKey(auth)
	if apiKey == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "mimo-code: missing JWT token (bootstrap failed)"}
		return
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	// Apply MiMo-specific payload transformations
	translated = applyMiMoReasoningBackfill(translated)

	url := mimoCodeBaseURL(auth) + mimoCodeChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	applyMiMoCodeHeaders(httpReq, apiKey)
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
			log.Errorf("mimo-code executor: close response body error: %v", errClose)
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
		return resp, fmt.Errorf("mimo-code: marshal response: %w", err)
	}

	reporter.publish(ctx, parseOpenAIUsage(body))
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream handles streaming execution against the MiMo Code API.
func (e *MiMoCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	baseModel = resolveMiMoCodeModelName(e.cfg, auth, baseModel)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	apiKey := mimoCodeAPIKey(auth)
	if apiKey == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "mimo-code: missing JWT token (bootstrap failed)"}
		return nil, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)

	// Apply MiMo-specific payload transformations
	translated = applyMiMoReasoningBackfill(translated)

	url := mimoCodeBaseURL(auth) + mimoCodeChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	applyMiMoCodeHeaders(httpReq, apiKey)
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
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("mimo-code executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("mimo-code executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
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
					recordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.publishFailure(ctx)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				continue
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(trimmedLine), &param)
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
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		reporter.ensurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens is not supported for MiMo Code.
func (e *MiMoCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("mimo-code: count tokens not supported")
}

// Refresh is not needed for MiMo Code (uses static ClientID).
func (e *MiMoCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// mimoCodeBaseURL returns the base URL for MiMo Code API.
func mimoCodeBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	return mimoCodeDefaultBaseURL
}

// mimoCodeAPIKey retrieves or bootstraps the JWT token for MiMo Code.
// The auth.Attributes["client_id"] is used as the key for bootstrap.
func mimoCodeAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	clientID := strings.TrimSpace(auth.Attributes["client_id"])
	if clientID == "" {
		return ""
	}
	// Note: JWT bootstrap is handled by the caller or middleware
	// Here we expect the JWT to already be in auth.Attributes["api_key"]
	return strings.TrimSpace(auth.Attributes["api_key"])
}

// applyMiMoCodeHeaders applies MiMo Code specific headers to the request.
func applyMiMoCodeHeaders(req *http.Request, jwt string) {
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}
	req.Header.Set("X-Mimo-Source", mimoCodeSourceHeader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli-proxy-mimo-code")
}

// resolveMiMoCodeModelName resolves the model name from config mapping.
func resolveMiMoCodeModelName(cfg *config.Config, auth *cliproxyauth.Auth, model string) string {
	if cfg == nil || auth == nil {
		return model
	}
	for i := range cfg.MiMoCodeKey {
		entry := &cfg.MiMoCodeKey[i]
		if entry.ClientID == "" {
			continue
		}
		if auth.Attributes != nil && auth.Attributes["client_id"] == entry.ClientID {
			for _, m := range entry.Models {
				if m.Alias == model {
					return m.Name
				}
			}
		}
	}
	return model
}

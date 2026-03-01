package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	clineauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	clineVersion        = "3.0.0"
	clineBaseURL        = "https://api.cline.bot/api/v1"
	clineModelsEndpoint = "/ai/cline/models"
	clineChatEndpoint   = "/chat/completions"
)

func clineTokenAuthValue(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	if strings.HasPrefix(t, "workos:") {
		return "Bearer " + t
	}
	return "Bearer workos:" + t
}

// ClineExecutor handles requests to Cline API.
type ClineExecutor struct {
	cfg *config.Config
}

// NewClineExecutor creates a new Cline executor instance.
func NewClineExecutor(cfg *config.Config) *ClineExecutor {
	return &ClineExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *ClineExecutor) Identifier() string { return "cline" }

// PrepareRequest prepares the HTTP request before execution.
func (e *ClineExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, err := e.ensureFreshAccessToken(req.Context(), auth)
	if err != nil {
		return err
	}
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("cline: missing access token")
	}

	req.Header.Set("Authorization", clineTokenAuthValue(accessToken))

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest executes a raw HTTP request.
func (e *ClineExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("cline executor: request is nil")
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

// Execute performs a non-streaming request.
func (e *ClineExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, err := e.ensureFreshAccessToken(ctx, auth)
	if err != nil {
		return resp, err
	}
	if accessToken == "" {
		return resp, fmt.Errorf("cline: missing access token")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := clineChatEndpoint

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, opts.Stream)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)
	translated = applyClineOpenRouterParity(translated, false)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	url := clineBaseURL + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	applyClineHeaders(httpReq, accessToken, false)

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
	defer httpResp.Body.Close()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
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
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

// ExecuteStream performs a streaming request.
func (e *ClineExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, err := e.ensureFreshAccessToken(ctx, auth)
	if err != nil {
		return nil, err
	}
	if accessToken == "" {
		return nil, fmt.Errorf("cline: missing access token")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := clineChatEndpoint

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)
	translated = applyClineOpenRouterParity(translated, true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	url := clineBaseURL + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	applyClineHeaders(httpReq, accessToken, true)

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
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		httpResp.Body.Close()
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer httpResp.Body.Close()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			if len(line) == 0 {
				continue
			}
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// Refresh validates the Cline token.
func (e *ClineExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("missing auth")
	}
	return auth, nil
}

// CountTokens returns the token count for the given request.
func (e *ClineExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("cline: count tokens not supported")
}

// clineAccessToken extracts access token from auth.
func clineAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}

	// Check metadata first, then attributes
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["accessToken"].(string); ok && token != "" {
			return token
		}
		if token, ok := auth.Metadata["access_token"].(string); ok && token != "" {
			return token
		}
		if token, ok := auth.Metadata["token"].(string); ok && token != "" {
			return token
		}
	}

	if auth.Attributes != nil {
		if token := auth.Attributes["accessToken"]; token != "" {
			return token
		}
		if token := auth.Attributes["access_token"]; token != "" {
			return token
		}
		if token := auth.Attributes["token"]; token != "" {
			return token
		}
	}

	return ""
}

func clineRefreshToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["refreshToken"].(string); ok && strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token)
		}
		if token, ok := auth.Metadata["refresh_token"].(string); ok && strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token)
		}
	}
	if auth.Attributes != nil {
		if token := strings.TrimSpace(auth.Attributes["refreshToken"]); token != "" {
			return token
		}
		if token := strings.TrimSpace(auth.Attributes["refresh_token"]); token != "" {
			return token
		}
	}
	return ""
}

func (e *ClineExecutor) ensureFreshAccessToken(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	accessToken := clineAccessToken(auth)
	if strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf("cline: missing access token")
	}

	refreshToken := clineRefreshToken(auth)
	if refreshToken == "" {
		return accessToken, nil
	}

	authSvc := clineauth.NewClineAuth(e.cfg)
	refreshed, err := authSvc.RefreshToken(ctx, refreshToken)
	if err != nil {
		log.Warnf("cline: token refresh failed, fallback to current token: %v", err)
		return accessToken, nil
	}
	if refreshed == nil || strings.TrimSpace(refreshed.AccessToken) == "" {
		return accessToken, nil
	}

	newAccessToken := strings.TrimSpace(refreshed.AccessToken)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["accessToken"] = newAccessToken
	auth.Metadata["access_token"] = newAccessToken

	if strings.TrimSpace(refreshed.RefreshToken) != "" {
		newRefresh := strings.TrimSpace(refreshed.RefreshToken)
		auth.Metadata["refreshToken"] = newRefresh
		auth.Metadata["refresh_token"] = newRefresh
	}

	if strings.TrimSpace(refreshed.ExpiresAt) != "" {
		if t, parseErr := time.Parse(time.RFC3339Nano, refreshed.ExpiresAt); parseErr == nil {
			auth.Metadata["expiresAt"] = t.Unix()
			auth.Metadata["expires_at"] = t.Format(time.RFC3339)
		} else if t, parseErr2 := time.Parse(time.RFC3339, refreshed.ExpiresAt); parseErr2 == nil {
			auth.Metadata["expiresAt"] = t.Unix()
			auth.Metadata["expires_at"] = t.Format(time.RFC3339)
		}
	}

	return newAccessToken, nil
}

// applyClineHeaders sets the standard Cline headers.
func applyClineHeaders(r *http.Request, token string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", clineTokenAuthValue(token))
	r.Header.Set("HTTP-Referer", "https://cline.bot")
	r.Header.Set("X-Title", "Cline")
	r.Header.Set("X-Task-ID", "")
	r.Header.Set("X-CLIENT-TYPE", "cli")
	r.Header.Set("X-CORE-VERSION", clineVersion)
	r.Header.Set("X-IS-MULTIROOT", "false")
	r.Header.Set("X-CLIENT-VERSION", clineVersion)
	r.Header.Set("X-PLATFORM", runtime.GOOS)
	r.Header.Set("X-PLATFORM-VERSION", runtime.Version())
	r.Header.Set("User-Agent", "Cline/"+clineVersion)
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("Cache-Control", "no-cache")
	} else {
		r.Header.Set("Accept", "application/json")
	}
}

func applyClineOpenRouterParity(payload []byte, stream bool) []byte {
	if len(payload) == 0 {
		return payload
	}

	out := payload
	if stream {
		if updated, err := sjson.SetRawBytes(out, "stream_options", []byte(`{"include_usage":true}`)); err == nil {
			out = updated
		}
		if updated, err := sjson.SetBytes(out, "include_reasoning", true); err == nil {
			out = updated
		}
	} else {
		if updated, err := sjson.DeleteBytes(out, "stream_options"); err == nil {
			out = updated
		}
		if updated, err := sjson.SetBytes(out, "include_reasoning", true); err == nil {
			out = updated
		}
	}

	modelID := strings.TrimSpace(gjson.GetBytes(out, "model").String())
	if modelID == "" {
		return out
	}

	if strings.Contains(modelID, "kwaipilot/kat-coder-pro") {
		trimmedModel := strings.TrimSuffix(modelID, ":free")
		if updated, err := sjson.SetBytes(out, "model", trimmedModel); err == nil {
			out = updated
		}
		if updated, err := sjson.SetRawBytes(out, "provider", []byte(`{"sort":"throughput"}`)); err == nil {
			out = updated
		}
	}

	return out
}

// ClineModel represents a model from Cline API.
type ClineModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MaxTokens   int    `json:"max_tokens"`
	ContextLen  int    `json:"context_length"`
	Pricing     struct {
		Prompt         string `json:"prompt"`
		Completion     string `json:"completion"`
		InputCacheRead string `json:"input_cache_read"`
		WebSearch      string `json:"web_search"`
	} `json:"pricing"`
}

func clineIsFreeModel(m ClineModel) bool {
	promptRaw := strings.TrimSpace(m.Pricing.Prompt)
	completionRaw := strings.TrimSpace(m.Pricing.Completion)
	if promptRaw == "" || completionRaw == "" {
		return false
	}
	promptPrice, errPrompt := strconv.ParseFloat(promptRaw, 64)
	completionPrice, errCompletion := strconv.ParseFloat(completionRaw, 64)
	if errPrompt != nil || errCompletion != nil {
		return false
	}
	return promptPrice == 0 && completionPrice == 0
}

// FetchClineModels fetches models from Cline API.
// The model list endpoint does not require authentication.
func FetchClineModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	log.Debugf("cline: fetching dynamic models from API")

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clineBaseURL+clineModelsEndpoint, nil)
	if err != nil {
		log.Warnf("cline: failed to create model fetch request: %v", err)
		return nil
	}

	req.Header.Set("User-Agent", "Cline/"+clineVersion)
	req.Header.Set("HTTP-Referer", "https://cline.bot")
	req.Header.Set("X-Title", "Cline")
	req.Header.Set("X-CLIENT-TYPE", "cli")
	req.Header.Set("X-CORE-VERSION", clineVersion)
	req.Header.Set("X-IS-MULTIROOT", "false")
	req.Header.Set("X-CLIENT-VERSION", clineVersion)
	req.Header.Set("X-PLATFORM", runtime.GOOS)
	req.Header.Set("X-PLATFORM-VERSION", runtime.Version())

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("cline: fetch models canceled: %v", err)
		} else {
			log.Warnf("cline: fetch models failed: %v", err)
		}
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("cline: failed to read models response: %v", err)
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		log.Warnf("cline: fetch models failed: status %d, body: %s", resp.StatusCode, string(body))
		return nil
	}

	// Parse models response
	var modelsResponse struct {
		Data []ClineModel `json:"data"`
	}
	if err := json.Unmarshal(body, &modelsResponse); err != nil {
		log.Warnf("cline: failed to parse models response: %v", err)
		return nil
	}

	// Also try gjson parsing as fallback
	if len(modelsResponse.Data) == 0 {
		result := gjson.GetBytes(body, "data")
		if !result.Exists() {
			// Try root if data field is missing
			result = gjson.ParseBytes(body)
			if !result.IsArray() {
				log.Debugf("cline: response body: %s", string(body))
				log.Warn("cline: invalid API response format (expected array or data field with array)")
				return nil
			}
		}
		result.ForEach(func(key, value gjson.Result) bool {
			id := value.Get("id").String()
			if id == "" {
				return true
			}
			modelsResponse.Data = append(modelsResponse.Data, ClineModel{
				ID:         id,
				Name:       value.Get("name").String(),
				ContextLen: int(value.Get("context_length").Int()),
				MaxTokens:  int(value.Get("max_tokens").Int()),
				Pricing: struct {
					Prompt         string `json:"prompt"`
					Completion     string `json:"completion"`
					InputCacheRead string `json:"input_cache_read"`
					WebSearch      string `json:"web_search"`
				}{
					Prompt:         value.Get("pricing.prompt").String(),
					Completion:     value.Get("pricing.completion").String(),
					InputCacheRead: value.Get("pricing.input_cache_read").String(),
					WebSearch:      value.Get("pricing.web_search").String(),
				},
			})
			return true
		})
	}

	now := time.Now().Unix()
	var dynamicModels []*registry.ModelInfo
	count := 0

	for _, m := range modelsResponse.Data {
		if m.ID == "" {
			continue
		}
		if !clineIsFreeModel(m) {
			continue
		}
		contextLen := m.ContextLen
		if contextLen == 0 {
			contextLen = 200000 // Default context length
		}
		maxTokens := m.MaxTokens
		if maxTokens == 0 {
			maxTokens = 64000 // Default max tokens
		}
		displayName := m.Name
		if displayName == "" {
			displayName = m.ID
		}

		dynamicModels = append(dynamicModels, &registry.ModelInfo{
			ID:                  m.ID,
			DisplayName:         displayName,
			Description:         m.Description,
			ContextLength:       contextLen,
			MaxCompletionTokens: maxTokens,
			OwnedBy:             "cline",
			Type:                "cline",
			Object:              "model",
			Created:             now,
		})
		count++
	}

	log.Infof("cline: fetched %d free models from API", count)
	return dynamicModels
}

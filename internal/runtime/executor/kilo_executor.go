package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	kiloVersion      = "3.26.0"
	kiloTesterHeader = "X-Kilocode-Tester"
	kiloEditorHeader = "X-KiloCode-EditorName"

	// KiloAnonymousAPIKey is the literal bearer sent when no real credential is
	// available. Probed live: https://api.kilo.ai/api/openrouter/chat/completions
	// accepts `Authorization: Bearer anonymous` for the free tier without signup.
	KiloAnonymousAPIKey = "anonymous"

	// KiloAnonymousAuthID identifies the synthesized no-credential auth used when
	// callers route through the Kilo free tier without an OAuth credential.
	KiloAnonymousAuthID = "kilo-anonymous"

	// KiloGatewayAnonymousAuthID identifies the synthesized optional-auth entry
	// for kilo-gateway's free catalog.
	KiloGatewayAnonymousAuthID = "kilo-gateway-anonymous"
)

// KiloExecutor handles requests to Kilo API.
type KiloExecutor struct {
	cfg        *config.Config
	identifier string
}

// NewKiloExecutor creates a new Kilo executor instance.
func NewKiloExecutor(cfg *config.Config) *KiloExecutor {
	return NewKiloExecutorForProvider(cfg, "kilo")
}

// NewKiloExecutorForProvider creates a Kilo executor bound to a specific
// provider identifier. kilo-gateway shares the same request shape but uses a
// different chat-completions path and optional auth.
func NewKiloExecutorForProvider(cfg *config.Config, identifier string) *KiloExecutor {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		identifier = "kilo"
	}
	return &KiloExecutor{cfg: cfg, identifier: identifier}
}

// NewKiloAnonymousAuth returns a synthesized no-credential auth used to call
// Kilo's free tier without a connected account. The auth is never persisted.
func NewKiloAnonymousAuth() *cliproxyauth.Auth {
	return newKiloAnonymousAuth("kilo", KiloAnonymousAuthID, KiloAnonymousAPIKey)
}

// NewKiloGatewayAnonymousAuth returns a synthesized optional-auth entry for the
// kilo-gateway free catalog. The gateway answers without Authorization.
func NewKiloGatewayAnonymousAuth() *cliproxyauth.Auth {
	return newKiloAnonymousAuth("kilo-gateway", KiloGatewayAnonymousAuthID, "")
}

func newKiloAnonymousAuth(provider, id, token string) *cliproxyauth.Auth {
	metadata := map[string]any{"type": provider}
	if token != "" {
		metadata["kilocodeToken"] = token
	}
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: provider,
		Status:   cliproxyauth.StatusActive,
		Attributes: map[string]string{
			cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth,
			"kilo_anonymous":               "true",
		},
		Metadata: metadata,
	}
}

func (e *KiloExecutor) kiloResolveAccessToken(auth *cliproxyauth.Auth) string {
	token, _ := kiloCredentials(auth)
	if token != "" && token != KiloAnonymousAPIKey {
		return token
	}
	if e != nil && e.identifier == "kilo-gateway" {
		return token
	}
	if token == "" {
		return KiloAnonymousAPIKey
	}
	return token
}
func (e *KiloExecutor) Identifier() string {
	if e == nil || e.identifier == "" {
		return "kilo"
	}
	return e.identifier
}

func (e *KiloExecutor) chatCompletionsPath() string {
	if e != nil && e.identifier == "kilo-gateway" {
		return "/api/gateway/chat/completions"
	}
	return "/api/openrouter/chat/completions"
}

// PrepareRequest prepares the HTTP request before execution.
func (e *KiloExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken := e.kiloResolveAccessToken(auth)

	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	} else {
		req.Header.Del("Authorization")
	}
	if req.Header.Get(kiloEditorHeader) == "" {
		req.Header.Set(kiloEditorHeader, "CLIProxyAPIPlus")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest executes a raw HTTP request.
func (e *KiloExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kilo executor: request is nil")
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
func (e *KiloExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, orgID := kiloCredentials(auth)
	if accessToken == "" {
		accessToken = e.kiloResolveAccessToken(auth)
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := e.chatCompletionsPath()

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, opts.Stream)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	url := "https://api.kilo.ai" + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	applyKiloHeaders(httpReq, accessToken, orgID, false)
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
func (e *KiloExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, orgID := kiloCredentials(auth)
	if accessToken == "" {
		accessToken = e.kiloResolveAccessToken(auth)
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := e.chatCompletionsPath()

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	url := "https://api.kilo.ai" + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	applyKiloHeaders(httpReq, accessToken, orgID, true)

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
				if !sendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}) {
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			if !sendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Err: errScan}) {
				return
			}
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// Refresh validates the Kilo token.
func (e *KiloExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("missing auth")
	}
	return auth, nil
}

// CountTokens returns the token count for the given request.
func (e *KiloExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("kilo: count tokens not supported")
}

// kiloCredentials extracts access token and other info from auth.
func kiloCredentials(auth *cliproxyauth.Auth) (accessToken, orgID string) {
	if auth == nil {
		return "", ""
	}

	// Prefer kilocode-specific keys first, then fall back to generic keys.
	// Check metadata first, then attributes.
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["kilocodeToken"].(string); ok && token != "" {
			accessToken = token
		} else if token, ok := auth.Metadata["token"].(string); ok && token != "" {
			accessToken = token
		} else if token, ok := auth.Metadata["access_token"].(string); ok && token != "" {
			accessToken = token
		}

		if org, ok := auth.Metadata["kilocodeOrganizationId"].(string); ok && org != "" {
			orgID = org
		} else if org, ok := auth.Metadata["organization_id"].(string); ok && org != "" {
			orgID = org
		}
	}

	if accessToken == "" && auth.Attributes != nil {
		if token := auth.Attributes["kilocodeToken"]; token != "" {
			accessToken = token
		} else if token := auth.Attributes["token"]; token != "" {
			accessToken = token
		} else if token := auth.Attributes["access_token"]; token != "" {
			accessToken = token
		}
	}

	if orgID == "" && auth.Attributes != nil {
		if org := auth.Attributes["kilocodeOrganizationId"]; org != "" {
			orgID = org
		} else if org := auth.Attributes["organization_id"]; org != "" {
			orgID = org
		}
	}

	return accessToken, orgID
}

// FetchKiloModels fetches the live Kilo OpenRouter catalog. When no credential
// is present the request still goes out with `Bearer anonymous` so the free
// tier remains visible. Failures fall back to the static catalog.
func FetchKiloModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	return FilterKiloModels(fetchKiloModels(ctx, auth, cfg, "https://api.kilo.ai/api/openrouter/models", "kilo", registry.GetKiloModels()))
}

// FetchKiloGatewayModels fetches the live kilo-gateway catalog. Auth is
// optional: the gateway answers without an Authorization header. Anonymous
// (no-credential) callers can only reach the gateway free tier, so their
// advertised catalog is filtered to free models; credentialed auths keep the
// full list.
func FetchKiloGatewayModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	return fetchKiloGatewayModels(ctx, auth, cfg, "https://api.kilo.ai/api/gateway/models")
}

func fetchKiloGatewayModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config, modelsURL string) []*registry.ModelInfo {
	models := fetchKiloModels(ctx, auth, cfg, modelsURL, "kilo-gateway", registry.GetKiloGatewayModels())
	if KiloGatewayAuthIsAnonymous(auth) {
		return FilterKiloModels(models)
	}
	return models
}

// KiloGatewayAuthIsAnonymous reports whether the gateway auth carries no real
// credential. Synthesized anonymous entries and token-less auths can only use
// the gateway free tier, so the models they advertise must be free-only.
func KiloGatewayAuthIsAnonymous(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(auth.Attributes["kilo_anonymous"]), "true") {
		return true
	}
	token, _ := kiloCredentials(auth)
	return token == "" || token == KiloAnonymousAPIKey
}

func fetchKiloModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config, modelsURL, ownedBy string, fallback []*registry.ModelInfo) []*registry.ModelInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	accessToken, orgID := kiloCredentials(auth)
	if accessToken == "" && ownedBy == "kilo" {
		accessToken = KiloAnonymousAPIKey
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		log.Warnf("%s: failed to create model fetch request: %v", ownedBy, err)
		return fallback
	}

	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	if orgID != "" {
		req.Header.Set("X-Kilocode-OrganizationID", orgID)
	}
	req.Header.Set(kiloEditorHeader, "CLIProxyAPIPlus")
	req.Header.Set("User-Agent", "cli-proxy-kilo")

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("%s: fetch models canceled: %v", ownedBy, err)
		} else {
			log.Warnf("%s: using static models (API fetch failed: %v)", ownedBy, err)
		}
		return fallback
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("%s: failed to read models response: %v", ownedBy, err)
		return fallback
	}

	if resp.StatusCode != http.StatusOK {
		log.Warnf("%s: fetch models failed: status %d, body: %s", ownedBy, resp.StatusCode, string(body))
		return fallback
	}

	result := gjson.GetBytes(body, "data")
	if !result.Exists() {
		result = gjson.ParseBytes(body)
		if !result.IsArray() {
			log.Warnf("%s: invalid API response format (expected array or data field with array)", ownedBy)
			return fallback
		}
	}

	now := time.Now().Unix()
	seen := make(map[string]struct{})
	merged := make([]*registry.ModelInfo, 0, len(fallback)+8)
	for _, model := range fallback {
		if model == nil || model.ID == "" {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		merged = append(merged, model)
	}

	result.ForEach(func(_, value gjson.Result) bool {
		id := strings.TrimSpace(value.Get("id").String())
		if id == "" {
			return true
		}
		if _, exists := seen[id]; exists {
			return true
		}
		seen[id] = struct{}{}
		displayName := strings.TrimSpace(value.Get("name").String())
		if displayName == "" {
			displayName = id
		}
		merged = append(merged, &registry.ModelInfo{
			ID:                  id,
			DisplayName:         displayName,
			ContextLength:       int(value.Get("context_length").Int()),
			MaxCompletionTokens: int(value.Get("max_tokens").Int()),
			OwnedBy:             ownedBy,
			Type:                ownedBy,
			Object:              "model",
			Created:             now,
		})
		return true
	})

	if len(merged) == 0 {
		return fallback
	}
	return merged
}

func applyKiloHeaders(r *http.Request, token, orgID string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if orgID != "" {
		r.Header.Set("X-Kilocode-OrganizationID", orgID)
	}
	r.Header.Set("HTTP-Referer", "https://kilocode.ai")
	r.Header.Set("X-Title", "Kilo Code")
	r.Header.Set("X-KiloCode-Version", kiloVersion)
	r.Header.Set("User-Agent", "Kilo-Code/"+kiloVersion)
	r.Header.Set(kiloTesterHeader, "SUPPRESS")
	r.Header.Set(kiloEditorHeader, "CLIProxyAPIPlus")
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("Cache-Control", "no-cache")
	} else {
		r.Header.Set("Accept", "application/json")
	}
}

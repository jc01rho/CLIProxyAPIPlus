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
	"sync"
	"time"

	clineauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	clineBaseURL            = "https://api.cline.bot/api/v1"
	clineModelsEndpoint     = "/ai/cline/models"
	clineChatEndpoint       = "/chat/completions"
	clineModelsFetchTimeout = 5 * time.Second

	// Client-identification versions pinned to the official Cline CLI (npm
	// `cline`), extracted from its binary's header builder: X-CLIENT-VERSION
	// carries the CLI release and X-CORE-VERSION the bundled @cline/core
	// release. Cline's API answers unknown or stale client versions with the
	// misleading 401 "Please make sure you're using the latest version of
	// Cline", so these must track a real published CLI release.
	clineClientVersionPinned = "3.0.57" // npm `cline`
	clineCoreVersion         = "0.0.78" // npm `@cline/core` bundled with it
)

// clineRefreshLocks enforces a per-auth single-flight around refresh-before-
// expiry so concurrent requests against the same auth cannot both spend the
// rotating refresh token. The map is bounded by auth.ID lifetime.
var (
	clineRefreshMu    sync.Mutex
	clineRefreshLocks = make(map[string]*sync.Mutex)
)

func clineLockFor(id string) *sync.Mutex {
	clineRefreshMu.Lock()
	defer clineRefreshMu.Unlock()
	if id == "" {
		var empty sync.Mutex
		return &empty
	}
	if m, ok := clineRefreshLocks[id]; ok {
		return m
	}
	m := &sync.Mutex{}
	clineRefreshLocks[id] = m
	return m
}

// persistClineAuth writes the current auth record back through the active
// token store so rotated tokens survive process restart.
func persistClineAuth(ctx context.Context, auth *cliproxyauth.Auth) error {
	if auth == nil {
		return nil
	}
	store := sdkauth.GetTokenStore()
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := store.Save(ctx, auth); err != nil {
		log.Debugf("cline: persist rotated auth failed: %v", err)
		return err
	}
	return nil
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
	accessToken := e.authToken(auth)
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("cline: missing access token")
	}

	req.Header.Set("Authorization", clineauth.GetBearerHeaderValue(accessToken))

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

	if err := e.prepareAuth(ctx, auth); err != nil {
		return resp, err
	}
	accessToken := e.authToken(auth)
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

	httpResp, err := e.sendClineChat(ctx, auth, url, translated, false)
	if err != nil {
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

	if err := e.prepareAuth(ctx, auth); err != nil {
		return nil, err
	}
	accessToken := e.authToken(auth)
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

	httpResp, err := e.sendClineChat(ctx, auth, url, translated, true)
	if err != nil {
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

// Refresh validates the Cline token; when the token is near expiry the
// executor triggers an expiry-aware rotation via the refresh endpoint
// and writes the rotated values back into auth.Metadata.
func (e *ClineExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("missing auth")
	}
	if err := e.prepareAuth(ctx, auth); err != nil {
		return nil, err
	}
	// Ensure expiry metadata is seeded on first use.
	if e.authToken(auth) != "" {
		seedClineExpiryMetadata(auth)
	}
	return auth, nil
}

// CountTokens returns the token count for the given request.
func (e *ClineExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("cline: count tokens not supported")
}

// prepareAuth ensures the token stored in auth.Metadata carries the workos:
// prefix required by the Cline API. It also triggers an expiry-aware refresh
// whenever the token is near (or past) expiry; Cline access tokens are
// short-lived, and sending an expired one yields the generic 401
// "Please make sure you're using the latest version of Cline".
func (e *ClineExecutor) prepareAuth(ctx context.Context, auth *cliproxyauth.Auth) error {
	if e.shouldRefresh(auth) {
		if err := e.refreshBeforeExpiry(ctx, auth, false); err != nil {
			log.Warnf("cline: pre-request expiry-aware refresh failed: %v", err)
		}
	}
	return nil
}

func (e *ClineExecutor) shouldRefresh(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	expiryRaw := clineauth.MetadataExpiry(auth.Metadata)
	if expiryRaw > 0 {
		return clineauth.ShouldRefresh(expiryRaw)
	}
	// Treat unset expiry as stale enough to retry once; the refresh will
	// re-seed it.
	return true
}

// refreshBeforeExpiry swaps the in-memory token for a freshly rotated one
// using the refresh token available in the auth metadata. It enforces a
// per-auth single-flight so concurrent requests cannot rotate the same
// refresh token twice. After a successful rotation the new tokens are
// persisted through the active token store so they survive a restart. The
// auth metadata is the single source of truth so reload-from-disk flows
// (where auth.Storage is nil) still find the refresh token. When force is
// set (upstream rejected a token we still consider fresh) the freshness
// re-check is skipped.
func (e *ClineExecutor) refreshBeforeExpiry(ctx context.Context, auth *cliproxyauth.Auth, force bool) error {
	if e.cfg == nil || auth == nil {
		return nil
	}
	if auth.Metadata == nil {
		return nil
	}

	lock := clineLockFor(auth.ID)
	lock.Lock()
	defer lock.Unlock()

	// Re-check after acquiring; another goroutine may have already rotated.
	// A forced rotation (401 from upstream) bypasses this: the server has
	// rejected a token our local expiry state still considers valid.
	if !force && e.authToken(auth) != "" && !clineauth.ShouldRefresh(effectiveExpiry(auth)) {
		return nil
	}

	rt := e.refreshToken(auth)
	if strings.TrimSpace(rt) == "" {
		return fmt.Errorf("cline refresh token missing from auth metadata")
	}

	authSvc := clineauth.NewClineAuth(e.cfg)
	refreshed, err := authSvc.RefreshToken(ctx, rt)
	if err != nil {
		return err
	}
	if refreshed == nil || strings.TrimSpace(refreshed.AccessToken) == "" {
		return fmt.Errorf("empty refreshed access token")
	}

	newAccess := clineauth.NormalizeAccessToken(strings.TrimSpace(refreshed.AccessToken))
	storage, ok := auth.Storage.(*clineauth.ClineTokenStorage)
	if ok && storage != nil {
		storage.AccessToken = strings.TrimPrefix(newAccess, clineauth.WorkOSPrefix())
		if strings.TrimSpace(refreshed.RefreshToken) != "" {
			storage.RefreshToken = strings.TrimSpace(refreshed.RefreshToken)
		}
		expiresAt := clineauth.ParseExpiresAt(refreshed.ExpiresAt)
		if expiresAt > 0 {
			storage.ExpiresAt = expiresAt
		}
		if email := strings.TrimSpace(refreshed.UserEmail()); email != "" {
			storage.Email = email
		}
		clineauth.SyncMetadata(storage, auth.Metadata)
	} else {
		// Reload path: synthesize a ClineTokenStorage from the new payload so
		// SyncMetadata produces both snake_case and camelCase keys for the
		// filestore to round-trip back.
		synth := &clineauth.ClineTokenStorage{
			AccessToken:  strings.TrimPrefix(newAccess, clineauth.WorkOSPrefix()),
			RefreshToken: strings.TrimSpace(refreshed.RefreshToken),
			ExpiresAt:    clineauth.ParseExpiresAt(refreshed.ExpiresAt),
			Email:        strings.TrimSpace(refreshed.UserEmail()),
			Type:         "cline",
		}
		clineauth.SyncMetadata(synth, auth.Metadata)
		auth.Storage = synth
	}
	auth.Metadata["workos_prefixed"] = true

	return persistClineAuth(ctx, auth)
}

// effectiveExpiry returns the expiry timestamp from auth.Metadata or
// the in-memory storage as a best-effort fallback. It tolerates the numeric
// JSON types (int64/int/float64/json.Number/numeric string) a disk reload can
// produce under both camelCase and snake_case keys.
func effectiveExpiry(auth *cliproxyauth.Auth) int64 {
	if auth == nil || auth.Metadata == nil {
		return 0
	}
	if v := clineauth.MetadataExpiry(auth.Metadata); v > 0 {
		return v
	}
	storage, ok := auth.Storage.(*clineauth.ClineTokenStorage)
	if !ok || storage == nil {
		return 0
	}
	return storage.ExpiresAt
}

func (e *ClineExecutor) authToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if token, ok := auth.Metadata["accessToken"].(string); ok && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	if token, ok := auth.Metadata["access_token"].(string); ok && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	if token, ok := auth.Metadata["token"].(string); ok && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	return ""
}

func (e *ClineExecutor) refreshToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if token, ok := auth.Metadata["refreshToken"].(string); ok && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	if token, ok := auth.Metadata["refresh_token"].(string); ok && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	return ""
}

// seedClineExpiryMetadata seeds auth.Metadata["expiresAt"] from the
// persisted token storage when it is not already present.
func seedClineExpiryMetadata(auth *cliproxyauth.Auth) {
	if auth == nil || auth.Metadata == nil {
		return
	}
	if auth.Metadata["expiresAt"] != nil {
		return
	}
	storage, ok := auth.Storage.(*clineauth.ClineTokenStorage)
	if !ok || storage == nil || storage.ExpiresAt <= 0 {
		return
	}
	auth.Metadata["expiresAt"] = storage.ExpiresAt
}

// sendClineChat POSTs the translated chat payload to the Cline API. When the
// upstream rejects the access token with 401 — Cline's generic response for
// expired or rotated tokens, phrased "Please make sure you're using the
// latest version of Cline" — it force-rotates the token through the refresh
// endpoint (single-flight per auth) and replays the request once, mirroring
// 9Router's chatCore 401 refresh-and-retry path. Every response, including a
// non-2xx after the retry, is returned for the caller's status handling.
func (e *ClineExecutor) sendClineChat(ctx context.Context, auth *cliproxyauth.Auth, url string, payload []byte, stream bool) (*http.Response, error) {
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	send := func() (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		applyClineHeaders(httpReq, e.authToken(auth), stream)
		util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
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
		resp, err := httpClient.Do(httpReq)
		if err != nil {
			recordAPIResponseError(ctx, e.cfg, err)
		}
		return resp, err
	}

	resp, err := send()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	rejected, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	appendAPIResponseChunk(ctx, e.cfg, rejected)
	recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())

	if rotateErr := e.refreshBeforeExpiry(ctx, auth, true); rotateErr != nil {
		log.Warnf("cline: access token rejected with 401 and rotation failed: %v", rotateErr)
		return nil, statusErr{
			code: http.StatusUnauthorized,
			msg:  fmt.Sprintf("cline: access token rejected (401 %s); token refresh failed: %v. Re-authenticate this Cline account (server --cline-login or management UI) to restore access", clineRejectedMessage(rejected), rotateErr),
		}
	}
	log.Info("cline: access token rejected with 401; token rotated, retrying once")
	return send()
}

// clineRejectedMessage extracts the upstream error text from a rejected 401
// body, bounded in length for error propagation.
func clineRejectedMessage(body []byte) string {
	msg := strings.TrimSpace(gjson.GetBytes(body, "error").String())
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// applyClineHeaders sets the client-identification headers the Cline API
// expects, mirroring the official CLI's header builder (extracted from the
// npm `cline` binary): User-Agent `Cline/<cli version>`, X-CLIENT-VERSION
// `<cli version>`, X-CORE-VERSION `<@cline/core version>`, X-CLIENT-TYPE
// `cline-<source>`. X-Task-ID is omitted when no session id is tracked, matching
// the official client, which only sends it bound to a session.
func applyClineHeaders(r *http.Request, token string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", clineauth.GetBearerHeaderValue(token))
	r.Header.Set("HTTP-Referer", "https://cline.bot")
	r.Header.Set("X-Title", "Cline")
	r.Header.Set("X-CLIENT-TYPE", "cline-cli")
	r.Header.Set("X-CORE-VERSION", clineCoreVersion)
	r.Header.Set("X-IS-MULTIROOT", "false")
	r.Header.Set("X-CLIENT-VERSION", clineClientVersionPinned)
	r.Header.Set("X-PLATFORM", runtime.GOOS)
	r.Header.Set("X-PLATFORM-VERSION", clineClientVersionPinned)
	r.Header.Set("User-Agent", "Cline/"+clineClientVersionPinned)
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
	return fetchClineModels(ctx, auth, cfg, clineBaseURL+clineModelsEndpoint)
}

func fetchClineModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config, modelsURL string) []*registry.ModelInfo {
	log.Debugf("cline: fetching dynamic models from API")

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, clineModelsFetchTimeout)
	defer cancel()

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		log.Warnf("cline: failed to create model fetch request: %v", err)
		return nil
	}

	req.Header.Set("User-Agent", "Cline/"+clineClientVersionPinned)
	req.Header.Set("HTTP-Referer", "https://cline.bot")
	req.Header.Set("X-Title", "Cline")
	req.Header.Set("X-CLIENT-TYPE", "cline-cli")
	req.Header.Set("X-CORE-VERSION", clineCoreVersion)
	req.Header.Set("X-IS-MULTIROOT", "false")
	req.Header.Set("X-CLIENT-VERSION", clineClientVersionPinned)
	req.Header.Set("X-PLATFORM", runtime.GOOS)
	req.Header.Set("X-PLATFORM-VERSION", clineClientVersionPinned)

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

	// Also accept gjson parsing for OpenRouter-style top-level arrays.
	if len(modelsResponse.Data) == 0 {
		result := gjson.GetBytes(body, "data")
		if !result.Exists() {
			result = gjson.ParseBytes(body)
			if !result.IsArray() {
				log.Debugf("cline: response body: %s", string(body))
				log.Warn("cline: invalid API response format (expected array or data field with array)")
				return nil
			}
		}
		result.ForEach(func(_, value gjson.Result) bool {
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

	for _, m := range modelsResponse.Data {
		if m.ID == "" {
			continue
		}
		contextLen := m.ContextLen
		if contextLen == 0 {
			contextLen = 200000
		}
		maxTokens := m.MaxTokens
		if maxTokens == 0 {
			maxTokens = 64000
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
	}

	freeOnly := cfg != nil && cfg.ClineFreeModelsOnly
	dynamicModels = FilterClineModels(dynamicModels, freeOnly)
	log.Infof("cline: fetched %d models from API", len(dynamicModels))
	return dynamicModels
}

// FilterClineModels limits a Cline catalog to IDs containing ":free" when enabled.
func FilterClineModels(models []*registry.ModelInfo, freeOnly bool) []*registry.ModelInfo {
	if !freeOnly {
		return models
	}
	filtered := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model != nil && strings.Contains(model.ID, ":free") {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// WorkOSPrefix returns the prefix Cline expects on its access tokens.
func WorkOSPrefix() string { return clineauth.WorkOSPrefix() }

// GetBearerHeaderValue exposes the shared helper for testability.
func GetBearerHeaderValue(token string) string { return clineauth.GetBearerHeaderValue(token) }

// NormalizeAccessToken exposes token normalization for testability.
func NormalizeAccessToken(token string) string { return clineauth.NormalizeAccessToken(token) }

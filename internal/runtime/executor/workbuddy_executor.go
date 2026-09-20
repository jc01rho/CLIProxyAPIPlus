package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/workbuddy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const workBuddyAuthType = "workbuddy"

// workBuddyBaseURL is mutable for tests; production uses the WorkBuddy global boundary.
var workBuddyBaseURL = workbuddy.BaseURL

// workBuddyChatPaths is tried in order. Global accounts prefer the console path and
// fall back to the /v2 path only when the primary returns 404 or 405.
var workBuddyChatPaths = []string{"/console/chat/completions", "/v2/chat/completions"}

// WorkBuddyExecutor handles requests to the WorkBuddy global realm API.
type WorkBuddyExecutor struct {
	cfg *config.Config
}

// NewWorkBuddyExecutor creates a new WorkBuddy executor instance.
func NewWorkBuddyExecutor(cfg *config.Config) *WorkBuddyExecutor {
	return &WorkBuddyExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *WorkBuddyExecutor) Identifier() string { return workBuddyAuthType }

// workBuddyCredentials extracts the request identity from auth metadata.
func workBuddyCredentials(auth *cliproxyauth.Auth) (accessToken, userID, enterpriseID, domain string) {
	if auth == nil {
		return "", "", "", ""
	}
	accessToken = metaStringValue(auth.Metadata, "access_token")
	userID = metaStringValue(auth.Metadata, "uid")
	enterpriseID = metaStringValue(auth.Metadata, "enterprise_id")
	domain = metaStringValue(auth.Metadata, "domain")
	if domain == "" {
		domain = workbuddy.DefaultDomain
	}
	return
}

// PrepareRequest prepares the HTTP request before execution.
func (e *WorkBuddyExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, userID, enterpriseID, domain := workBuddyCredentials(auth)
	if accessToken == "" {
		return fmt.Errorf("workbuddy: missing access token")
	}
	e.applyHeaders(req, accessToken, userID, enterpriseID, domain)
	return nil
}

// HttpRequest executes a raw HTTP request.
func (e *WorkBuddyExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("workbuddy executor: request is nil")
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

// applyHeaders applies the WorkBuddy request identity and attribution headers.
func (e *WorkBuddyExecutor) applyHeaders(req *http.Request, accessToken, userID, enterpriseID, domain string) {
	if req == nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-Product", "WorkBuddy")
	req.Header.Set("X-CodeBuddy-Request", "1")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", workbuddy.BaseURL)
	req.Header.Set("Referer", workbuddy.BaseURL+"/")
	req.Header.Set("Accept-Language", "en-US")
	req.Header.Set("User-Agent", workbuddy.UserAgent)
	req.Header.Set("X-Agent-Purpose", "conversation")
	req.Header.Set("X-IDE-Name", "WorkBuddy")
	req.Header.Set("X-IDE-Type", "WorkBuddy")
	req.Header.Set("X-IDE-Version", "5.5.4")
	if accessToken == "" {
		req.Header.Set("X-No-Authorization", "1")
	}
	if userID == "" {
		req.Header.Set("X-No-User-Id", "1")
	}
	if enterpriseID == "" {
		req.Header.Set("X-No-Enterprise-Id", "1")
		req.Header.Set("X-No-Department-Info", "1")
	} else {
		req.Header.Set("X-Enterprise-Id", enterpriseID)
	}
}

// translatePayload normalizes the client payload for the WorkBuddy upstream.
func (e *WorkBuddyExecutor) translatePayload(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, baseModel string) ([]byte, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)
	var supportedEfforts []string
	defaultEffort := ""
	if modelInfo := registry.LookupModelInfo(baseModel, workBuddyAuthType); modelInfo != nil && strings.EqualFold(modelInfo.Type, workBuddyAuthType) && modelInfo.Thinking != nil {
		supportedEfforts = modelInfo.Thinking.Levels
		defaultEffort = modelInfo.Thinking.DefaultEffort
	}
	translated, _ = helps.NormalizeWorkBuddyPayload(translated, supportedEfforts, defaultEffort)
	return ensureWorkBuddySystemMessage(translated), nil
}

// ensureWorkBuddySystemMessage prepends the neutral system message the WorkBuddy
// global console endpoint expects when the client did not supply one.
func ensureWorkBuddySystemMessage(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return payload
	}
	if strings.EqualFold(messages.Array()[0].Get("role").String(), "system") {
		return payload
	}
	prepended := `[{"role":"system","content":"You are a helpful assistant."},` + messages.Raw[1:]
	updated, err := sjson.SetRawBytes(payload, "messages", []byte(prepended))
	if err != nil {
		return payload
	}
	return updated
}

// Execute performs a non-streaming request, aggregating the upstream SSE stream.
func (e *WorkBuddyExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, userID, enterpriseID, domain := workBuddyCredentials(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("workbuddy: missing access token")
	}

	translated, err := e.translatePayload(ctx, req, opts, baseModel)
	if err != nil {
		return resp, err
	}
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	httpResp, err := e.chatRequest(ctx, auth, translated, accessToken, userID, enterpriseID, domain)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("workbuddy executor: close response body error: %v", errClose)
		}
	}()

	if !isHTTPSuccess(httpResp.StatusCode) {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		return resp, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), b)}
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, body)
	aggregatedBody, usageDetail, err := aggregateOpenAIChatCompletionStream(body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	reporter.publish(ctx, usageDetail)
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, aggregatedBody, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out), Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming request.
func (e *WorkBuddyExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, userID, enterpriseID, domain := workBuddyCredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("workbuddy: missing access token")
	}

	translated, err := e.translatePayload(ctx, req, opts, baseModel)
	if err != nil {
		return nil, err
	}
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	httpResp, err := e.chatRequest(ctx, auth, translated, accessToken, userID, enterpriseID, domain)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	if !isHTTPSuccess(httpResp.StatusCode) {
		body, _ := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		appendAPIResponseChunk(ctx, e.cfg, body)
		return nil, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), body)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("workbuddy executor: close stream body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, maxScannerBufferSize)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(line), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}:
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
			return
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// chatRequest posts the payload, falling back to the secondary chat path on 404/405.
func (e *WorkBuddyExecutor) chatRequest(ctx context.Context, auth *cliproxyauth.Auth, payload []byte, accessToken, userID, enterpriseID, domain string) (*http.Response, error) {
	for i, path := range workBuddyChatPaths {
		url := workBuddyBaseURL + path
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		e.applyHeaders(httpReq, accessToken, userID, enterpriseID, domain)
		httpReq.Header.Set("Accept", "application/json, text/event-stream")
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
			return nil, err
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

		isLast := i == len(workBuddyChatPaths)-1
		if !isLast && (httpResp.StatusCode == http.StatusNotFound || httpResp.StatusCode == http.StatusMethodNotAllowed) {
			_ = httpResp.Body.Close()
			continue
		}
		return httpResp, nil
	}
	return nil, fmt.Errorf("workbuddy: no chat path available")
}

// Refresh exchanges the refresh token for a new access token and returns the updated auth.
func (e *WorkBuddyExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("workbuddy: missing auth")
	}
	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		log.Debugf("workbuddy executor: no refresh token available, skipping refresh")
		return auth, nil
	}

	accessToken, userID, _, domain := workBuddyCredentials(auth)
	authSvc := workbuddy.NewWorkBuddyAuth(e.cfg)
	storage, err := authSvc.RefreshToken(ctx, accessToken, refreshToken, userID, domain)
	if err != nil {
		return nil, fmt.Errorf("workbuddy: token refresh failed: %w", err)
	}

	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	updated.Metadata["access_token"] = storage.AccessToken
	if storage.RefreshToken != "" {
		updated.Metadata["refresh_token"] = storage.RefreshToken
	}
	updated.Metadata["expires_at"] = storage.ExpiresAt
	updated.Metadata["domain"] = storage.Domain
	if storage.UserID != "" {
		updated.Metadata["uid"] = storage.UserID
	}
	if storage.EnterpriseID != "" {
		updated.Metadata["enterprise_id"] = storage.EnterpriseID
	}
	if storage.Nickname != "" {
		updated.Metadata["nickname"] = storage.Nickname
	}
	return updated, nil
}

// CountTokens is unsupported: WorkBuddy has no token counting endpoint.
func (e *WorkBuddyExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("workbuddy: count tokens not supported")
}

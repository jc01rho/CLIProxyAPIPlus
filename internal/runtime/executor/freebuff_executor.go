package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	freebuffDefaultBaseURL = "https://www.codebuff.com"
	freebuffUserAgent      = "ai-sdk/openai-compatible/1.0.0/codebuff"
	freebuffMarker         = "You are Buffy, the strategic coding assistant."
	freebuffMaxErrorBody   = 1 << 20
	freebuffMaxNonStream   = 64 << 20
	freebuffCleanupTimeout = 5 * time.Second
	freebuffHeartbeatEvery = 45 * time.Second
)

type FreebuffExecutor struct {
	provider string
	cfg      *config.Config
}

var freebuffCredentialStates = struct {
	sync.Mutex
	states map[string]*freebuffCredentialState
}{states: make(map[string]*freebuffCredentialState)}

type freebuffCredentialState struct {
	lease   chan struct{}
	mu      sync.Mutex
	session *freebuffSession
	refs    int
	retired bool
}

type freebuffSession struct {
	model      string
	instanceID string
	expiresAt  time.Time
}

type freebuffRun struct {
	id        string
	startedAt time.Time
}

func NewFreebuffExecutor(cfg *config.Config) *FreebuffExecutor {
	return &FreebuffExecutor{provider: "freebuff", cfg: cfg}
}

func (e *FreebuffExecutor) Identifier() string { return e.provider }

func (e *FreebuffExecutor) CloseExecutionSession(sessionID string) {
	if sessionID != cliproxyauth.CloseAllExecutionSessionsID {
		return
	}
	freebuffCredentialStates.Lock()
	defer freebuffCredentialStates.Unlock()
	for key, state := range freebuffCredentialStates.states {
		if state.refs == 0 {
			delete(freebuffCredentialStates.states, key)
			continue
		}
		state.retired = true
	}
}

func (e *FreebuffExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("freebuff executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	req = req.Clone(ctx)
	req.Header = req.Header.Clone()
	key := freebuffAPIKey(auth)
	if key == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "freebuff: missing API key"}
	}
	util.ApplyCustomHeadersFromAttrs(req, freebuffAttrs(auth))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", freebuffUserAgent)
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
}

func (e *FreebuffExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	model, agentID := e.resolveModel(auth, baseModel)
	if model == "" {
		err = statusErr{code: http.StatusBadRequest, msg: "freebuff: unsupported model"}
		return
	}
	if agentID == "" {
		err = statusErr{code: http.StatusBadRequest, msg: "freebuff: model has no agent mapping"}
		return
	}
	if freebuffAPIKey(auth) == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "freebuff: missing API key"}
		return
	}
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), model, auth)
	defer reporter.TrackFailure(ctx, &err)

	state, stateKey, stateErr := e.acquireCredentialState(ctx, auth)
	if stateErr != nil {
		err = stateErr
		return resp, err
	}
	defer e.releaseCredentialState(stateKey, state, true)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	baseTranslated := sdktranslator.TranslateRequest(from, to, model, bytes.Clone(req.Payload), true)
	var (
		session    *freebuffSession
		run        *freebuffRun
		translated []byte
		httpResp   *http.Response
	)
	for attempt := 0; ; attempt++ {
		session, run, translated, httpResp, err = e.openChat(ctx, auth, state, model, agentID, baseTranslated)
		if err != nil {
			return resp, err
		}
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			break
		}
		body, _ := readFreebuffBody(httpResp.Body, freebuffMaxErrorBody)
		_ = httpResp.Body.Close()
		upstreamErr := freebuffHTTPError(httpResp.StatusCode, body, retryAfter(httpResp.Header))
		e.finishRunDetached(ctx, auth, run, "failed", "", upstreamErr)
		if attempt == 0 && isFreebuffEndingSessionError(httpResp.StatusCode, body) {
			e.invalidateSession(state)
			continue
		}
		err = upstreamErr
		return resp, err
	}
	defer httpResp.Body.Close()
	defer e.startSessionHeartbeat(ctx, auth, state, model, session.instanceID)()
	finished := false
	finish := func(status string, completionID string, finishErr error) {
		if finished {
			return
		}
		finished = true
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), freebuffCleanupTimeout)
		defer cancel()
		_ = e.finishRun(finishCtx, auth, run, status, completionID, finishErr)
	}
	defer func() {
		if err != nil {
			status := "failed"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				status = "cancelled"
			}
			finish(status, "", err)
		}
	}()

	acc := newFreebuffAccumulator()
	totalBytes := 0
	if err = consumeFreebuffSSE(httpResp.Body, func(raw []byte, chunk map[string]any) error {
		totalBytes += len(raw)
		if totalBytes > freebuffMaxNonStream {
			return statusErr{code: http.StatusBadGateway, msg: "freebuff: non-stream response exceeds 64 MiB"}
		}
		acc.add(chunk)
		if usage, ok := helps.ParseOpenAIStreamUsage(raw); ok {
			reporter.Publish(ctx, usage)
		}
		return nil
	}); err != nil {
		return resp, err
	}
	body := acc.response(model)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	finish("completed", acc.id, nil)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *FreebuffExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	model, agentID := e.resolveModel(auth, baseModel)
	if model == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "freebuff: unsupported model"}
	}
	if agentID == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "freebuff: model has no agent mapping"}
	}
	if freebuffAPIKey(auth) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "freebuff: missing API key"}
	}
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), model, auth)
	defer reporter.TrackFailure(ctx, &err)
	state, stateKey, stateErr := e.acquireCredentialState(ctx, auth)
	if stateErr != nil {
		err = stateErr
		return nil, err
	}
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	baseTranslated := sdktranslator.TranslateRequest(from, to, model, bytes.Clone(req.Payload), true)
	var (
		session    *freebuffSession
		run        *freebuffRun
		translated []byte
		httpResp   *http.Response
	)
	for attempt := 0; ; attempt++ {
		session, run, translated, httpResp, err = e.openChat(ctx, auth, state, model, agentID, baseTranslated)
		if err != nil {
			e.releaseCredentialState(stateKey, state, true)
			return nil, err
		}
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			break
		}
		body, _ := readFreebuffBody(httpResp.Body, freebuffMaxErrorBody)
		_ = httpResp.Body.Close()
		upstreamErr := freebuffHTTPError(httpResp.StatusCode, body, retryAfter(httpResp.Header))
		e.finishRunDetached(ctx, auth, run, "failed", "", upstreamErr)
		if attempt == 0 && isFreebuffEndingSessionError(httpResp.StatusCode, body) {
			e.invalidateSession(state)
			continue
		}
		e.releaseCredentialState(stateKey, state, true)
		return nil, upstreamErr
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer e.releaseCredentialState(stateKey, state, true)
		defer httpResp.Body.Close()
		defer e.startSessionHeartbeat(ctx, auth, state, model, session.instanceID)()
		completed := false
		var lastID string
		var param any
		streamErr := consumeFreebuffSSE(httpResp.Body, func(raw []byte, chunk map[string]any) error {
			if id, ok := chunk["id"].(string); ok {
				lastID = id
			}
			if usage, ok := helps.ParseOpenAIStreamUsage(raw); ok {
				reporter.Publish(ctx, usage)
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, raw, &param)
			for _, payload := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if hasFinishedChunk(chunk) {
				completed = true
			}
			return nil
		})
		status := "completed"
		var finishErr error
		if streamErr != nil {
			finishErr = streamErr
			if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
				status = "cancelled"
			} else {
				status = "failed"
			}
			reporter.PublishFailure(ctx, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
		} else if !completed {
			finishErr = errors.New("freebuff: stream ended without finish reason")
			status = "failed"
			reporter.PublishFailure(ctx, finishErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: finishErr}:
			case <-ctx.Done():
			}
		} else {
			reporter.EnsurePublished(ctx)
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), freebuffCleanupTimeout)
		defer cancel()
		if finishErr != nil && errors.Is(ctx.Err(), context.Canceled) {
			status = "cancelled"
		}
		if errFinish := e.finishRun(finishCtx, auth, run, status, lastID, finishErr); errFinish != nil {
			log.Debugf("freebuff executor: finish run failed: %v", errFinish)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *FreebuffExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *FreebuffExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("freebuff: count tokens not supported")
}

func (e *FreebuffExecutor) stateFor(auth *cliproxyauth.Auth) *freebuffCredentialState {
	key := freebuffCredentialKey(auth)
	freebuffCredentialStates.Lock()
	defer freebuffCredentialStates.Unlock()
	if state := freebuffCredentialStates.states[key]; state != nil {
		return state
	}
	state := &freebuffCredentialState{lease: make(chan struct{}, 1)}
	freebuffCredentialStates.states[key] = state
	return state
}

func freebuffCredentialKey(auth *cliproxyauth.Auth) string {
	token := freebuffAPIKey(auth)
	baseURL := freebuffDefaultBaseURL
	if auth != nil && auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			baseURL = normalizeFreebuffBaseURL(value)
		}
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]) + "\x00" + baseURL
}

func (e *FreebuffExecutor) acquireCredentialState(ctx context.Context, auth *cliproxyauth.Auth) (*freebuffCredentialState, string, error) {
	key := freebuffCredentialKey(auth)
	freebuffCredentialStates.Lock()
	state := freebuffCredentialStates.states[key]
	if state == nil {
		state = &freebuffCredentialState{lease: make(chan struct{}, 1)}
		freebuffCredentialStates.states[key] = state
	}
	state.refs++
	freebuffCredentialStates.Unlock()
	if err := state.acquire(ctx); err != nil {
		e.releaseCredentialState(key, state, false)
		return nil, "", err
	}
	return state, key, nil
}

func (e *FreebuffExecutor) releaseCredentialState(key string, state *freebuffCredentialState, releaseLease bool) {
	if releaseLease {
		state.release()
	}
	freebuffCredentialStates.Lock()
	defer freebuffCredentialStates.Unlock()
	if state.refs > 0 {
		state.refs--
	}
	if state.refs == 0 && state.retired && freebuffCredentialStates.states[key] == state {
		delete(freebuffCredentialStates.states, key)
	}
}

func (s *freebuffCredentialState) acquire(ctx context.Context) error {
	select {
	case s.lease <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *freebuffCredentialState) release() {
	select {
	case <-s.lease:
	default:
	}
}

func (e *FreebuffExecutor) resolveModel(auth *cliproxyauth.Auth, model string) (string, string) {
	model = strings.TrimSpace(model)
	apiKey := freebuffAPIKey(auth)
	if e.cfg != nil {
		keys := e.cfg.FreebuffKey
		if auth != nil && auth.Attributes != nil {
			if rawIndex := strings.TrimSpace(auth.Attributes["config_index"]); rawIndex != "" {
				if index, err := strconv.Atoi(rawIndex); err == nil && index >= 0 && index < len(keys) {
					keys = keys[index : index+1]
				}
			}
		}
		proxyURL := ""
		baseURL := ""
		if auth != nil {
			proxyURL = strings.TrimSpace(auth.ProxyURL)
			if auth.Attributes != nil {
				baseURL = strings.TrimSpace(auth.Attributes["base_url"])
			}
		}
		for _, key := range keys {
			if !key.MatchesCredential(apiKey, proxyURL) {
				continue
			}
			if configuredBaseURL := strings.TrimSpace(key.BaseURL); baseURL != "" && configuredBaseURL != baseURL {
				continue
			}
			for _, configured := range key.Models {
				if configured.Alias == model || configured.Name == model {
					return resolveFreebuffConfiguredModel(configured.Name, configured.AgentID, model)
				}
			}
		}
	}
	return lookupFreebuffBuiltin(model)
}

const freebuffDefaultModelID = "deepseek/deepseek-v4-pro"

type freebuffBuiltinModel struct {
	id    string
	agent string
}

var freebuffBuiltinModels = []freebuffBuiltinModel{
	{id: "deepseek/deepseek-v4-pro", agent: "base2-free-deepseek"},
	{id: "deepseek/deepseek-v4-flash", agent: "base2-free-deepseek-flash"},
	{id: "mimo/mimo-v2.5", agent: "base2-free-mimo"},
	{id: "minimax/minimax-m3", agent: "base2-free-minimax-m3"},
	{id: "z-ai/glm-5.2", agent: "base2-free-glm"},
	{id: "openai/gpt-5.6-luna", agent: "base2-free-luna"},
	{id: "anthropic/claude-fable-5", agent: "base2-free-fable"},
}

func isFreebuffProviderAlias(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", "freebuff", "codebuff":
		return true
	default:
		return false
	}
}

func freebuffModelShortName(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}

func lookupFreebuffBuiltin(model string) (string, string) {
	model = strings.TrimSpace(model)
	if isFreebuffProviderAlias(model) {
		return freebuffBuiltinModels[0].id, freebuffBuiltinModels[0].agent
	}
	lower := strings.ToLower(model)
	short := freebuffModelShortName(model)
	for _, item := range freebuffBuiltinModels {
		idLower := strings.ToLower(item.id)
		if lower == idLower || short == freebuffModelShortName(item.id) {
			return item.id, item.agent
		}
	}
	return model, ""
}

func resolveFreebuffConfiguredModel(name, agentID, requested string) (string, string) {
	if strings.TrimSpace(agentID) != "" {
		return name, agentID
	}
	canon, builtinAgent := lookupFreebuffBuiltin(name)
	if builtinAgent == "" {
		canon, builtinAgent = lookupFreebuffBuiltin(requested)
	}
	if builtinAgent == "" {
		return name, ""
	}
	if isFreebuffProviderAlias(name) {
		return canon, builtinAgent
	}
	if _, nameAgent := lookupFreebuffBuiltin(name); nameAgent != "" {
		return canon, builtinAgent
	}
	return name, builtinAgent
}

func (e *FreebuffExecutor) ensureSession(ctx context.Context, auth *cliproxyauth.Auth, state *freebuffCredentialState, model string) (*freebuffSession, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	skipLookup := false
	if state.session != nil && state.session.instanceID != "" {
		cached := state.session
		if cached.model != model {
			if _, _, err := e.sessionRequest(ctx, auth, http.MethodDelete, cached.model, cached.instanceID); err != nil {
				return nil, err
			}
			state.session = nil
			skipLookup = true
		} else {
			current, status, refreshErr := e.sessionRequest(ctx, auth, http.MethodGet, model, cached.instanceID)
			if refreshErr != nil {
				return nil, refreshErr
			}
			if status == "active" && current.instanceID != "" && current.model == model {
				state.session = current
				return current, nil
			}
			if status == "queued" && current.instanceID != "" {
				ready, err := e.waitForSession(ctx, auth, model, current.instanceID)
				if err != nil {
					return nil, err
				}
				state.session = ready
				return ready, nil
			}
			if current.instanceID != "" && (status == "active" || status == "model_locked" || current.model != model) {
				if _, _, err := e.sessionRequest(ctx, auth, http.MethodDelete, model, current.instanceID); err != nil {
					return nil, err
				}
				skipLookup = true
			}
			state.session = nil
		}
	}
	if !skipLookup {
		current, status, err := e.sessionRequest(ctx, auth, http.MethodGet, model, "")
		if err != nil {
			return nil, err
		}
		if status == "active" && current.instanceID != "" && current.model == model {
			state.session = current
			return current, nil
		}
		if status == "queued" && current.instanceID != "" {
			ready, waitErr := e.waitForSession(ctx, auth, model, current.instanceID)
			if waitErr != nil {
				return nil, waitErr
			}
			state.session = ready
			return ready, nil
		}
		if current.instanceID != "" && (status == "active" || status == "model_locked" || current.model != model) {
			if _, _, errDelete := e.sessionRequest(ctx, auth, http.MethodDelete, model, current.instanceID); errDelete != nil {
				return nil, errDelete
			}
		}
	}
	created, createdStatus, err := e.sessionRequest(ctx, auth, http.MethodPost, model, "")
	if err != nil {
		return nil, err
	}
	if created.instanceID == "" {
		return nil, statusErr{code: http.StatusBadGateway, msg: "freebuff: session response missing instanceId"}
	}
	if createdStatus == "queued" {
		ready, waitErr := e.waitForSession(ctx, auth, model, created.instanceID)
		if waitErr != nil {
			return nil, waitErr
		}
		state.session = ready
		return ready, nil
	}
	if createdStatus != "active" || created.model != model {
		cleanupCtx, cancelCleanup := freebuffCleanupContext(ctx)
		_, _, _ = e.sessionRequest(cleanupCtx, auth, http.MethodDelete, model, created.instanceID)
		cancelCleanup()
		return nil, statusErr{code: http.StatusBadGateway, msg: "freebuff: invalid session admission response"}
	}
	state.session = created
	return created, nil
}

func (e *FreebuffExecutor) openChat(ctx context.Context, auth *cliproxyauth.Auth, state *freebuffCredentialState, model, agentID string, basePayload []byte) (*freebuffSession, *freebuffRun, []byte, *http.Response, error) {
	session, err := e.ensureSession(ctx, auth, state, model)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	run, err := e.startRun(ctx, auth, agentID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	payload, err := buildFreebuffPayload(basePayload, model, agentID, run.id, session.instanceID)
	if err != nil {
		e.finishRunDetached(ctx, auth, run, "failed", "", err)
		return nil, nil, nil, nil, err
	}
	resp, err := e.chat(ctx, auth, model, session.instanceID, payload)
	if err != nil {
		status := "failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = "cancelled"
		}
		e.finishRunDetached(ctx, auth, run, status, "", err)
		return nil, nil, nil, nil, err
	}
	return session, run, payload, resp, nil
}

func (e *FreebuffExecutor) invalidateSession(state *freebuffCredentialState) {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.session = nil
	state.mu.Unlock()
}

func (e *FreebuffExecutor) waitForSession(ctx context.Context, auth *cliproxyauth.Auth, model, instanceID string) (ready *freebuffSession, err error) {
	cleanupInstanceID := instanceID
	retain := false
	defer func() {
		if retain || cleanupInstanceID == "" {
			return
		}
		cleanupCtx, cancelCleanup := freebuffCleanupContext(ctx)
		defer cancelCleanup()
		_, _, _ = e.sessionRequest(cleanupCtx, auth, http.MethodDelete, model, cleanupInstanceID)
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		current, status, err := e.sessionRequest(ctx, auth, http.MethodGet, model, instanceID)
		if err != nil {
			return nil, err
		}
		if current.instanceID != "" {
			cleanupInstanceID = current.instanceID
		}
		if status == "active" && current.instanceID != "" && current.model == model {
			retain = true
			return current, nil
		}
		if status == "active" && current.model != model {
			return nil, statusErr{code: http.StatusConflict, msg: "freebuff: queued session activated with a different model"}
		}
		if status != "queued" {
			return nil, statusErr{code: http.StatusBadGateway, msg: "freebuff: unexpected session queue status"}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, statusErr{code: http.StatusTooManyRequests, msg: "freebuff: session queued"}
		}
		wait := 250 * time.Millisecond
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *FreebuffExecutor) runSessionHeartbeat(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	state *freebuffCredentialState,
	model string,
	instanceID string,
	ticks <-chan time.Time,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			requestCtx, cancelRequest := context.WithTimeout(ctx, freebuffCleanupTimeout)
			current, status, err := e.sessionRequest(requestCtx, auth, http.MethodGet, model, instanceID)
			cancelRequest()
			if err != nil {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					log.Debugf("freebuff executor: session heartbeat failed: %v", err)
				}
				continue
			}
			if status != "active" || current.instanceID != instanceID || current.model != model {
				log.Debugf("freebuff executor: session heartbeat returned non-active state")
				continue
			}
			if ctx.Err() != nil {
				return
			}
			e.applyHeartbeatSession(state, model, instanceID, current)
		}
	}
}

func (e *FreebuffExecutor) startSessionHeartbeat(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	state *freebuffCredentialState,
	model string,
	instanceID string,
) func() {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(freebuffHeartbeatEvery)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		e.runSessionHeartbeat(heartbeatCtx, auth, state, model, instanceID, ticker.C)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (e *FreebuffExecutor) applyHeartbeatSession(
	state *freebuffCredentialState,
	model string,
	instanceID string,
	current *freebuffSession,
) {
	if state == nil || current == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.session == nil || state.session.model != model || state.session.instanceID != instanceID {
		return
	}
	state.session = current
}

func (e *FreebuffExecutor) sessionRequest(ctx context.Context, auth *cliproxyauth.Auth, method, model, instanceID string) (*freebuffSession, string, error) {
	url := e.baseURL(auth) + "/api/v1/freebuff/session"
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, "", err
	}
	util.ApplyCustomHeadersFromAttrs(req, freebuffAttrs(auth))
	req.Header.Set("Authorization", "Bearer "+freebuffAPIKey(auth))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-freebuff-model", model)
	req.Header.Set("User-Agent", freebuffUserAgent)
	if instanceID != "" {
		req.Header.Set("x-freebuff-instance-id", instanceID)
	}
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, _ := readFreebuffBody(resp.Body, freebuffMaxErrorBody)
	if method == http.MethodDelete && (resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK) {
		return &freebuffSession{}, "ended", nil
	}
	if method == http.MethodGet && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone) {
		return &freebuffSession{model: model}, "none", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", freebuffHTTPError(resp.StatusCode, body, retryAfter(resp.Header))
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, "", statusErr{code: http.StatusBadGateway, msg: "freebuff: malformed session response"}
	}
	if nested, ok := raw["data"].(map[string]any); ok &&
		stringValue(raw, "status") == "" && stringValue(raw, "state") == "" {
		raw = nested
	}
	status := stringValue(raw, "status")
	if status == "" {
		status = stringValue(raw, "state")
	}
	session := &freebuffSession{
		model:      firstString(raw, "model", "requestedModel"),
		instanceID: firstString(raw, "instanceId", "instance_id"),
		expiresAt:  parseExpiry(raw),
	}
	if session.model == "" {
		session.model = model
	}
	if status == "rate_limited" || status == "spend_limited" || status == "ip_capped" {
		return nil, status, statusErr{code: http.StatusTooManyRequests, msg: freebuffErrorMessage(body), retryAfter: retryAfterFromJSON(raw)}
	}
	if status == "country_blocked" || status == "banned" {
		return nil, status, statusErr{code: http.StatusForbidden, msg: freebuffErrorMessage(body)}
	}
	if status == "model_unavailable" {
		return nil, status, statusErr{code: http.StatusServiceUnavailable, msg: freebuffErrorMessage(body)}
	}
	if status == "premium_slot_taken" || status == "session_limit_reached" {
		return nil, status, statusErr{code: http.StatusConflict, msg: freebuffErrorMessage(body), retryAfter: retryAfterFromJSON(raw)}
	}
	switch status {
	case "active", "queued", "none", "ended", "superseded", "model_locked":
	default:
		return nil, status, statusErr{code: http.StatusBadGateway, msg: "freebuff: unknown session status"}
	}
	return session, status, nil
}

func (e *FreebuffExecutor) startRun(ctx context.Context, auth *cliproxyauth.Auth, agentID string) (*freebuffRun, error) {
	body := map[string]any{"action": "START", "agentId": agentID, "ancestorRunIds": []string{}}
	payload, _ := json.Marshal(body)
	raw, status, err := e.postJSON(ctx, auth, "/api/v1/agent-runs", payload)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, freebuffHTTPError(status, raw, nil)
	}
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: "freebuff: malformed START response"}
	}
	runID := firstString(response, "runId", "run_id", "id")
	if runID == "" {
		return nil, statusErr{code: http.StatusBadGateway, msg: "freebuff: START response missing runId"}
	}
	return &freebuffRun{id: runID, startedAt: time.Now()}, nil
}

func (e *FreebuffExecutor) finishRun(ctx context.Context, auth *cliproxyauth.Auth, run *freebuffRun, status, completionID string, runErr error) error {
	if run == nil || run.id == "" {
		return nil
	}
	stepStatus := "completed"
	if status != "completed" {
		stepStatus = status
	}
	steps := []any{}
	if status == "completed" {
		steps = append(steps, map[string]any{
			"id":          run.id + "-step",
			"stepNumber":  1,
			"credits":     0,
			"childRunIds": []string{},
			"messageId":   completionID,
			"status":      stepStatus,
			"startTime":   run.startedAt.UTC().Format(time.RFC3339),
		})
	}
	body := map[string]any{
		"action": "FINISH", "runId": run.id, "status": status,
		"totalSteps": 1, "directCredits": 0, "totalCredits": 0, "steps": steps,
	}
	if runErr != nil && status == "failed" {
		msg := runErr.Error()
		if len(msg) > 5000 {
			msg = msg[:5000]
		}
		body["errorMessage"] = msg
	}
	payload, _ := json.Marshal(body)
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		raw, code, err := e.postJSON(ctx, auth, "/api/v1/agent-runs", payload)
		if err != nil {
			last = err
			if attempt == 0 && ctx.Err() == nil {
				continue
			}
			return err
		}
		if code == http.StatusNotFound || code == http.StatusGone {
			return nil
		}
		if code >= 200 && code < 300 {
			return nil
		}
		last = freebuffHTTPError(code, raw, nil)
		if attempt == 0 && code >= 500 {
			continue
		}
		return last
	}
	return last
}

func (e *FreebuffExecutor) finishRunDetached(ctx context.Context, auth *cliproxyauth.Auth, run *freebuffRun, status, completionID string, runErr error) {
	finishCtx, cancel := freebuffCleanupContext(ctx)
	defer cancel()
	if err := e.finishRun(finishCtx, auth, run, status, completionID, runErr); err != nil {
		log.Debugf("freebuff executor: finish run failed: %v", err)
	}
}

func freebuffCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), freebuffCleanupTimeout)
}

func (e *FreebuffExecutor) chat(ctx context.Context, auth *cliproxyauth.Auth, model, instanceID string, payload []byte) (*http.Response, error) {
	url := e.baseURL(auth) + "/api/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	util.ApplyCustomHeadersFromAttrs(req, freebuffAttrs(auth))
	req.Header.Set("Authorization", "Bearer "+freebuffAPIKey(auth))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", freebuffUserAgent)
	req.Header.Set("x-freebuff-model", model)
	if instanceID != "" {
		req.Header.Set("x-freebuff-instance-id", instanceID)
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
}

func (e *FreebuffExecutor) postJSON(ctx context.Context, auth *cliproxyauth.Auth, path string, payload []byte) ([]byte, int, error) {
	url := e.baseURL(auth) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	util.ApplyCustomHeadersFromAttrs(req, freebuffAttrs(auth))
	req.Header.Set("Authorization", "Bearer "+freebuffAPIKey(auth))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", freebuffUserAgent)
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, readErr := readFreebuffBody(resp.Body, freebuffMaxErrorBody)
	return body, resp.StatusCode, readErr
}

func readFreebuffBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(body)) > limit {
		body = append(body[:limit], []byte("...[truncated]")...)
	}
	return body, err
}

func (e *FreebuffExecutor) baseURL(auth *cliproxyauth.Auth) string {
	if attrs := freebuffAttrs(auth); attrs != nil {
		if value := strings.TrimSpace(attrs["base_url"]); value != "" {
			return normalizeFreebuffBaseURL(value)
		}
	}
	return freebuffDefaultBaseURL
}

func normalizeFreebuffBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return freebuffDefaultBaseURL
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Path = strings.TrimRight(strings.TrimSuffix(parsed.Path, "/v1"), "/")
		return strings.TrimRight(parsed.String(), "/")
	}
	return strings.TrimRight(strings.TrimSuffix(raw, "/v1"), "/")
}

func freebuffAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["api_key"])
}

func freebuffAttrs(auth *cliproxyauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return auth.Attributes
}

func buildFreebuffPayload(input []byte, model, agentID, runID, instanceID string) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(input, &payload); err != nil {
		return nil, fmt.Errorf("freebuff: invalid translated request: %w", err)
	}
	delete(payload, "runId")
	delete(payload, "cost_mode")
	payload["model"] = model
	payload["stream"] = true
	payload["stream_options"] = map[string]any{"include_usage": true}
	payload["codebuff_metadata"] = map[string]any{
		"run_id": runID, "client_id": newFreebuffID(), "cost_mode": "free",
		"freebuff_instance_id": instanceID, "llm_step_number": "1", "n": agentID,
	}
	ensureBuffyMarker(&payload)
	return json.Marshal(payload)
}

func ensureBuffyMarker(payload *map[string]any) {
	messages, ok := (*payload)["messages"].([]any)
	if !ok {
		messages = []any{}
	}
	prefix := freebuffMarker + "\n\n"
	if len(messages) > 0 {
		if first, ok := messages[0].(map[string]any); ok && stringValue(first, "role") == "system" {
			first["content"] = prefixContent(first["content"], prefix)
			messages[0] = first
			(*payload)["messages"] = messages
			return
		}
	}
	messages = append([]any{map[string]any{"role": "system", "content": prefix[:len(prefix)-2]}}, messages...)
	(*payload)["messages"] = messages
}

func prefixContent(content any, prefix string) any {
	switch value := content.(type) {
	case string:
		if strings.HasPrefix(value, freebuffMarker) {
			return value
		}
		return prefix + value
	case []any:
		if len(value) > 0 {
			if part, ok := value[0].(map[string]any); ok && stringValue(part, "type") == "text" {
				text := stringValue(part, "text")
				if strings.HasPrefix(text, freebuffMarker) {
					return value
				}
				part["text"] = prefix + text
				value[0] = part
				return value
			}
		}
		return append([]any{map[string]any{"type": "text", "text": strings.TrimSuffix(prefix, "\n")}}, value...)
	default:
		return prefix + fmt.Sprint(value)
	}
}

func consumeFreebuffSSE(r io.Reader, fn func([]byte, map[string]any) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 50*1024*1024)
	var data []string
	var event string
	var eventBytes int
	sawDone := false
	sawFinish := false
	process := func() error {
		if len(data) == 0 {
			event = ""
			eventBytes = 0
			return nil
		}
		raw := []byte(strings.Join(data, "\n"))
		data = data[:0]
		eventBytes = 0
		if bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
			sawDone = true
			event = ""
			return fn([]byte("data: [DONE]"), nil)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			return statusErr{code: http.StatusBadGateway, msg: "freebuff: malformed SSE event"}
		}
		value = unwrapFreebuffChunk(value)
		if event == "error" || value["error"] != nil {
			return statusErr{code: http.StatusBadGateway, msg: freebuffErrorMessage(raw)}
		}
		normalizeFreebuffChunk(value)
		if hasFinishedChunk(value) {
			sawFinish = true
		}
		return fn(marshalOpenAIChunk(value), value)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if processErr := process(); processErr != nil {
				return processErr
			}
		} else if strings.HasPrefix(line, ":") {
			continue
		} else if strings.HasPrefix(line, "data:") {
			part := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			eventBytes += len(part)
			if eventBytes > 50*1024*1024 {
				return statusErr{code: http.StatusBadGateway, msg: "freebuff: SSE event exceeds 50 MiB"}
			}
			data = append(data, part)
		} else if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if sawDone {
			if !sawFinish {
				return statusErr{code: http.StatusBadGateway, msg: "freebuff: truncated SSE stream"}
			}
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return statusErr{code: http.StatusBadGateway, msg: "freebuff: SSE read failed"}
	}
	if processErr := process(); processErr != nil {
		return processErr
	}
	if !sawFinish || !sawDone {
		return statusErr{code: http.StatusBadGateway, msg: "freebuff: truncated SSE stream"}
	}
	return nil
}

func unwrapFreebuffChunk(value map[string]any) map[string]any {
	choices, hasChoices := value["choices"].([]any)
	if !hasChoices || len(choices) == 0 {
		if nested, ok := value["data"].(map[string]any); ok {
			if _, hasChoices := nested["choices"]; hasChoices || nested["error"] != nil {
				return nested
			}
		}
	}
	return value
}

func normalizeFreebuffChunk(value map[string]any) {
	choices, ok := value["choices"].([]any)
	if !ok {
		return
	}
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(choice, "finish_reason") == "tool-calls" {
			choice["finish_reason"] = "tool_calls"
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		if _, exists := delta["reasoning_content"]; !exists {
			if reasoning, exists := delta["reasoning"]; exists {
				delta["reasoning_content"] = reasoning
				delete(delta, "reasoning")
			}
		}
	}
}

func marshalOpenAIChunk(value map[string]any) []byte {
	body, _ := json.Marshal(value)
	return append([]byte("data: "), body...)
}

func hasFinishedChunk(value map[string]any) bool {
	choices, ok := value["choices"].([]any)
	if !ok {
		return false
	}
	for _, item := range choices {
		if choice, ok := item.(map[string]any); ok && stringValue(choice, "finish_reason") != "" && stringValue(choice, "finish_reason") != "null" {
			return true
		}
	}
	return false
}

type freebuffAccumulator struct {
	id       string
	model    string
	created  any
	choices  map[int]*freebuffAccumChoice
	usage    map[string]any
	finished bool
}

type freebuffAccumChoice struct {
	role      string
	content   strings.Builder
	reasoning strings.Builder
	details   []any
	finish    string
	tools     map[int]*freebuffAccumTool
}

type freebuffAccumTool struct {
	id, typ, name string
	args          strings.Builder
}

func newFreebuffAccumulator() *freebuffAccumulator {
	return &freebuffAccumulator{choices: map[int]*freebuffAccumChoice{}}
}

func (a *freebuffAccumulator) add(value map[string]any) {
	if id := stringValue(value, "id"); id != "" {
		a.id = id
	}
	if model := stringValue(value, "model"); model != "" {
		a.model = model
	}
	if created, ok := value["created"]; ok {
		a.created = created
	}
	if usage, ok := value["usage"].(map[string]any); ok {
		a.usage = usage
	}
	choices, _ := value["choices"].([]any)
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		index := intValue(choice["index"])
		acc := a.choices[index]
		if acc == nil {
			acc = &freebuffAccumChoice{tools: map[int]*freebuffAccumTool{}}
			a.choices[index] = acc
		}
		acc.finish = stringValue(choice, "finish_reason")
		delta, _ := choice["delta"].(map[string]any)
		if role := stringValue(delta, "role"); role != "" {
			acc.role = role
		}
		if content, ok := delta["content"].(string); ok {
			acc.content.WriteString(content)
		}
		if reasoning, ok := delta["reasoning_content"].(string); ok {
			acc.reasoning.WriteString(reasoning)
		}
		if details, ok := delta["reasoning_details"].([]any); ok {
			acc.details = append(acc.details, details...)
		}
		if tools, ok := delta["tool_calls"].([]any); ok {
			for _, rawTool := range tools {
				tool, ok := rawTool.(map[string]any)
				if !ok {
					continue
				}
				ti := intValue(tool["index"])
				entry := acc.tools[ti]
				if entry == nil {
					entry = &freebuffAccumTool{}
					acc.tools[ti] = entry
				}
				if id := stringValue(tool, "id"); id != "" {
					entry.id = appendFreebuffFragment(entry.id, id)
				}
				if typ := stringValue(tool, "type"); typ != "" {
					entry.typ = typ
				}
				if function, ok := tool["function"].(map[string]any); ok {
					if name := stringValue(function, "name"); name != "" {
						entry.name = appendFreebuffFragment(entry.name, name)
					}
					if args, ok := function["arguments"].(string); ok {
						entry.args.WriteString(args)
					}
				}
			}
		}
	}
}

func appendFreebuffFragment(current, fragment string) string {
	if current == "" {
		return fragment
	}
	if fragment == current || strings.HasSuffix(current, fragment) {
		return current
	}
	if strings.HasPrefix(fragment, current) {
		return fragment
	}
	return current + fragment
}

func (a *freebuffAccumulator) response(model string) []byte {
	if a.model == "" {
		a.model = model
	}
	indices := make([]int, 0, len(a.choices))
	for index := range a.choices {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	choices := make([]any, 0, len(indices))
	for _, index := range indices {
		acc := a.choices[index]
		message := map[string]any{"role": acc.role, "content": acc.content.String()}
		if message["role"] == "" {
			message["role"] = "assistant"
		}
		if acc.reasoning.Len() > 0 {
			message["reasoning_content"] = acc.reasoning.String()
		}
		if len(acc.details) > 0 {
			message["reasoning_details"] = acc.details
		}
		if len(acc.tools) > 0 {
			toolIndices := make([]int, 0, len(acc.tools))
			for toolIndex := range acc.tools {
				toolIndices = append(toolIndices, toolIndex)
			}
			sort.Ints(toolIndices)
			toolCalls := make([]any, 0, len(toolIndices))
			for _, toolIndex := range toolIndices {
				tool := acc.tools[toolIndex]
				toolCalls = append(toolCalls, map[string]any{
					"index": toolIndex, "id": tool.id, "type": tool.typ,
					"function": map[string]any{"name": tool.name, "arguments": tool.args.String()},
				})
			}
			message["tool_calls"] = toolCalls
		}
		finish := acc.finish
		if finish == "" {
			finish = "stop"
		}
		choices = append(choices, map[string]any{"index": index, "message": message, "finish_reason": finish})
	}
	body := map[string]any{
		"id": a.id, "object": "chat.completion", "created": a.created, "model": a.model,
		"choices": choices, "usage": a.usage,
	}
	out, _ := json.Marshal(body)
	return out
}

func stringValue(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	if text, ok := value[key].(string); ok {
		return text
	}
	return ""
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := stringValue(value, key); text != "" {
			return text
		}
	}
	return ""
}

func intValue(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case string:
		result, _ := strconv.Atoi(number)
		return result
	default:
		return 0
	}
}

func parseExpiry(raw map[string]any) time.Time {
	if value := firstString(raw, "expiresAt", "expires_at"); value != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed
		}
	}
	if remaining := intValue(raw["remainingMs"]); remaining > 0 {
		return time.Now().Add(time.Duration(remaining) * time.Millisecond)
	}
	return time.Time{}
}

func retryAfter(header http.Header) *time.Duration {
	if value := header.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil {
			duration := time.Duration(seconds) * time.Second
			return &duration
		}
	}
	return nil
}

func retryAfterFromJSON(raw map[string]any) *time.Duration {
	if value := intValue(raw["retryAfterMs"]); value > 0 {
		duration := time.Duration(value) * time.Millisecond
		return &duration
	}
	return nil
}

func freebuffHTTPError(code int, body []byte, retry *time.Duration) error {
	if code == http.StatusNotFound {
		return freebuffRequestError{statusErr{
			code:       http.StatusBadRequest,
			msg:        freebuffErrorMessage(body),
			retryAfter: retry,
		}}
	}
	return statusErr{code: code, msg: freebuffErrorMessage(body), retryAfter: retry}
}

type freebuffRequestError struct {
	statusErr
}

func (e freebuffRequestError) IsRequestScoped() bool { return true }

func freebuffErrorMessage(body []byte) string {
	if code := freebuffErrorCode(body); code != "" {
		return "freebuff: upstream error " + code
	}
	return "freebuff: upstream request failed"
}

func freebuffErrorCode(body []byte) string {
	var raw map[string]any
	if json.Unmarshal(body, &raw) != nil {
		return ""
	}
	for {
		if code := firstString(raw, "code", "error_code"); validFreebuffErrorCode(code) {
			return code
		}
		switch value := raw["error"].(type) {
		case string:
			if validFreebuffErrorCode(value) {
				return value
			}
		case map[string]any:
			raw = value
			continue
		}
		if nested, ok := raw["data"].(map[string]any); ok {
			raw = nested
			continue
		}
		return ""
	}
}

func isFreebuffEndingSessionError(status int, body []byte) bool {
	code := freebuffErrorCode(body)
	switch code {
	case "waiting_room_required":
		return status == http.StatusPreconditionRequired
	case "session_expired":
		return status == http.StatusGone
	case "session_superseded", "session_model_mismatch":
		return status == http.StatusConflict
	default:
		return false
	}
}

func validFreebuffErrorCode(value string) bool {
	if value == "" || len(value) > 96 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func newFreebuffID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return hex.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(raw[0:4]), hex.EncodeToString(raw[4:6]),
		hex.EncodeToString(raw[6:8]), hex.EncodeToString(raw[8:10]),
		hex.EncodeToString(raw[10:16]))
}

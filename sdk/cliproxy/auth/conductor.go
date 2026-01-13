package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// ProviderExecutor defines the contract required by Manager to execute provider calls.
type ProviderExecutor interface {
	// Identifier returns the provider key handled by this executor.
	Identifier() string
	// Execute handles non-streaming execution and returns the provider response payload.
	Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	// ExecuteStream handles streaming execution and returns a channel of provider chunks.
	ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error)
	// Refresh attempts to refresh provider credentials and returns the updated auth state.
	Refresh(ctx context.Context, auth *Auth) (*Auth, error)
	// CountTokens returns the token count for the given request.
	CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	// HttpRequest injects provider credentials into the supplied HTTP request and executes it.
	// Callers must close the response body when non-nil.
	HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error)
}

// RefreshEvaluator allows runtime state to override refresh decisions.
type RefreshEvaluator interface {
	ShouldRefresh(now time.Time, auth *Auth) bool
}

const (
	refreshCheckInterval  = 5 * time.Second
	refreshPendingBackoff = time.Minute
	refreshFailureBackoff = 1 * time.Minute
	quotaBackoffBase      = time.Second
	quotaBackoffMax       = 30 * time.Minute
)

var quotaCooldownDisabled atomic.Bool

// SetQuotaCooldownDisabled toggles quota cooldown scheduling globally.
func SetQuotaCooldownDisabled(disable bool) {
	quotaCooldownDisabled.Store(disable)
}

// Result captures execution outcome used to adjust auth state.
type Result struct {
	// AuthID references the auth that produced this result.
	AuthID string
	// Provider is copied for convenience when emitting hooks.
	Provider string
	// Model is the upstream model identifier used for the request.
	Model string
	// Success marks whether the execution succeeded.
	Success bool
	// RetryAfter carries a provider supplied retry hint (e.g. 429 retryDelay).
	RetryAfter *time.Duration
	// Error describes the failure when Success is false.
	Error *Error
}

// Selector chooses an auth candidate for execution.
type Selector interface {
	Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error)
}

// Hook captures lifecycle callbacks for observing auth changes.
type Hook interface {
	// OnAuthRegistered fires when a new auth is registered.
	OnAuthRegistered(ctx context.Context, auth *Auth)
	// OnAuthUpdated fires when an existing auth changes state.
	OnAuthUpdated(ctx context.Context, auth *Auth)
	// OnResult fires when execution result is recorded.
	OnResult(ctx context.Context, result Result)
}

// NoopHook provides optional hook defaults.
type NoopHook struct{}

// OnAuthRegistered implements Hook.
func (NoopHook) OnAuthRegistered(context.Context, *Auth) {}

// OnAuthUpdated implements Hook.
func (NoopHook) OnAuthUpdated(context.Context, *Auth) {}

// OnResult implements Hook.
func (NoopHook) OnResult(context.Context, Result) {}

// Manager orchestrates auth lifecycle, selection, execution, and persistence.
type Manager struct {
	store     Store
	executors map[string]ProviderExecutor
	selector  Selector
	hook      Hook
	mu        sync.RWMutex
	auths     map[string]*Auth
	// providerOffsets tracks per-model provider rotation state for multi-provider routing.
	providerOffsets map[string]int

	// Retry controls request retry behavior.
	requestRetry     atomic.Int32
	maxRetryInterval atomic.Int64

	// modelNameMappings stores global model name alias mappings (alias -> upstream name) keyed by channel.
	modelNameMappings atomic.Value

	// Optional HTTP RoundTripper provider injected by host.
	rtProvider RoundTripperProvider

	// Auto refresh state
	refreshCancel context.CancelFunc

	// Fallback configuration for model fallback when all keys are blocked.
	// fallbackModels maps model names to their fallback model.
	fallbackModels atomic.Value // map[string]string
	// fallbackChain is an ordered list of models to try in sequence.
	fallbackChain atomic.Value // []string
}

// NewManager constructs a manager with optional custom selector and hook.
func NewManager(store Store, selector Selector, hook Hook) *Manager {
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	if hook == nil {
		hook = NoopHook{}
	}
	return &Manager{
		store:           store,
		executors:       make(map[string]ProviderExecutor),
		selector:        selector,
		hook:            hook,
		auths:           make(map[string]*Auth),
		providerOffsets: make(map[string]int),
	}
}

func (m *Manager) SetSelector(selector Selector) {
	if m == nil {
		return
	}
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	m.mu.Lock()
	m.selector = selector
	m.mu.Unlock()
}

// SetStore swaps the underlying persistence store.
func (m *Manager) SetStore(store Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
}

// SetRoundTripperProvider register a provider that returns a per-auth RoundTripper.
func (m *Manager) SetRoundTripperProvider(p RoundTripperProvider) {
	m.mu.Lock()
	m.rtProvider = p
	m.mu.Unlock()
}

// SetRetryConfig updates retry attempts and cooldown wait interval.
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// SetFallbackConfig updates the fallback configuration for model failover.
// fallbackModels maps model names to their fallback model when all keys are blocked.
// fallbackChain is an ordered list of models to try in sequence.
func (m *Manager) SetFallbackConfig(fallbackModels map[string]string, fallbackChain []string) {
	if m == nil {
		return
	}
	if fallbackModels != nil {
		// Make a copy to avoid external mutation
		copied := make(map[string]string, len(fallbackModels))
		for k, v := range fallbackModels {
			copied[k] = v
		}
		m.fallbackModels.Store(copied)
	} else {
		m.fallbackModels.Store((map[string]string)(nil))
	}
	if fallbackChain != nil {
		// Make a copy to avoid external mutation
		copied := make([]string, len(fallbackChain))
		copy(copied, fallbackChain)
		m.fallbackChain.Store(copied)
	} else {
		m.fallbackChain.Store(([]string)(nil))
	}
}

// GetFallbackConfig returns the current fallback configuration.
func (m *Manager) GetFallbackConfig() (fallbackModels map[string]string, fallbackChain []string) {
	if m == nil {
		return nil, nil
	}
	if v := m.fallbackModels.Load(); v != nil {
		fallbackModels = v.(map[string]string)
	}
	if v := m.fallbackChain.Load(); v != nil {
		fallbackChain = v.([]string)
	}
	return fallbackModels, fallbackChain
}

// getFallbackModel returns the next fallback model for the given model.
// Returns empty string if no fallback is configured.
// Priority: model-specific fallback > fallback chain
func (m *Manager) getFallbackModel(model string, visited map[string]bool) string {
	if m == nil || model == "" {
		return ""
	}

	// Check model-specific fallback first
	if v := m.fallbackModels.Load(); v != nil {
		if fallbackModels, ok := v.(map[string]string); ok && fallbackModels != nil {
			if fallback, exists := fallbackModels[model]; exists && fallback != "" {
				if !visited[fallback] {
					return fallback
				}
			}
		}
	}

	// Check fallback chain
	if v := m.fallbackChain.Load(); v != nil {
		if chain, ok := v.([]string); ok && chain != nil {
			// Find current model in chain and return next
			for i, m := range chain {
				if m == model && i+1 < len(chain) {
					nextModel := chain[i+1]
					if !visited[nextModel] {
						return nextModel
					}
				}
			}
		}
	}

	return ""
}

// RegisterExecutor registers a provider executor with the manager.
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executors[executor.Identifier()] = executor
}

// UnregisterExecutor removes the executor associated with the provider key.
func (m *Manager) UnregisterExecutor(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	m.mu.Lock()
	delete(m.executors, provider)
	m.mu.Unlock()
}

// Register inserts a new auth entry into the manager.
func (m *Manager) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	auth.EnsureIndex()
	m.mu.Lock()
	m.auths[auth.ID] = auth.Clone()
	m.mu.Unlock()
	_ = m.persist(ctx, auth)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	return auth.Clone(), nil
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}
	m.mu.Lock()
	if existing, ok := m.auths[auth.ID]; ok && existing != nil && !auth.indexAssigned && auth.Index == "" {
		auth.Index = existing.Index
		auth.indexAssigned = existing.indexAssigned
	}
	auth.EnsureIndex()
	m.auths[auth.ID] = auth.Clone()
	m.mu.Unlock()
	_ = m.persist(ctx, auth)
	m.hook.OnAuthUpdated(ctx, auth.Clone())
	return auth.Clone(), nil
}

// Load resets manager state from the backing store.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil {
		return nil
	}
	items, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	m.auths = make(map[string]*Auth, len(items))
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		auth.EnsureIndex()
		m.auths[auth.ID] = auth.Clone()
	}
	return nil
}

// ValidateOnStartup checks all registered auths by making lightweight CountTokens calls.
// Auths that fail validation are marked as blocked for 2 hours.
// This helps identify quota-exceeded or invalid keys at startup before serving requests.
func (m *Manager) ValidateOnStartup(ctx context.Context) {
	m.mu.RLock()
	auths := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		auths = append(auths, auth.Clone())
	}
	m.mu.RUnlock()

	if len(auths) == 0 {
		log.Info("startup validation: no active auths to validate")
		return
	}

	log.Infof("startup validation: checking %d auth(s) for quota/validity...", len(auths))

	var wg sync.WaitGroup
	for _, auth := range auths {
		wg.Add(1)
		go func(a *Auth) {
			defer wg.Done()
			m.validateSingleAuth(ctx, a)
		}(auth)
	}
	wg.Wait()

	log.Info("startup validation: completed")
}

// validateSingleAuth performs a lightweight validation check on a single auth.
func (m *Manager) validateSingleAuth(ctx context.Context, auth *Auth) {
	if auth == nil || auth.ID == "" {
		return
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	executor := m.executorFor(provider)
	if executor == nil {
		log.Debugf("startup validation: no executor for auth %s (provider=%s), skipping", auth.ID, provider)
		return
	}

	// Use a short timeout for validation
	validateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Build a minimal CountTokens request
	req := cliproxyexecutor.Request{
		Model:   "gemini-2.0-flash", // Use a common model for validation
		Payload: []byte(`{"contents":[{"parts":[{"text":"test"}]}]}`),
	}

	// Try CountTokens as a lightweight validation
	_, err := executor.CountTokens(validateCtx, auth, req, cliproxyexecutor.Options{})

	if err != nil {
		// Check if it's a quota/auth error
		var statusErr cliproxyexecutor.StatusError
		statusCode := 0
		if errors.As(err, &statusErr) && statusErr != nil {
			statusCode = statusErr.StatusCode()
		}

		// Mark as blocked based on error type
		result := Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    req.Model,
			Success:  false,
			Error:    &Error{Message: err.Error(), HTTPStatus: statusCode},
		}
		m.MarkResult(ctx, result)

		log.Warnf("startup validation: auth %s (provider=%s) failed validation (status=%d): %v",
			auth.ID, auth.Provider, statusCode, err)
	} else {
		log.Infof("startup validation: auth %s (provider=%s) passed validation",
			auth.ID, auth.Provider)
	}
}

// Execute performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins across ALL keys
// regardless of provider, ensuring fair distribution proportional to key count.
// When all keys for a model are blocked (modelCooldownError), it tries fallback models.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return m.executeWithFallback(ctx, providers, req, opts, make(map[string]bool), 0)
}

// executeWithFallback performs execution with fallback model support.
// visited tracks models already tried to prevent infinite loops.
// depth tracks recursion depth to enforce maximum 20 fallback attempts.
func (m *Manager) executeWithFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, visited map[string]bool, depth int) (cliproxyexecutor.Response, error) {
	const maxFallbackDepth = 20

	// Mark current model as visited
	originalModel := req.Model
	visited[originalModel] = true

	// Try execution with current model
	resp, err := m.executeInternal(ctx, providers, req, opts)
	if err == nil {
		// Set actual model/provider if not already set
		if resp.ActualModel == "" {
			resp.ActualModel = req.Model
		}
		return resp, nil
	}

	// Check if this is a modelCooldownError and we should try fallback
	_, isCooldown := err.(*modelCooldownError)
	if !isCooldown {
		return resp, err
	}

	// Check depth limit
	if depth >= maxFallbackDepth {
		return resp, err
	}

	// Get fallback model
	fallbackModel := m.getFallbackModel(originalModel, visited)
	if fallbackModel == "" {
		return resp, err
	}

	// Try fallback model
	fallbackReq := req
	fallbackReq.Model = fallbackModel

	entry := logEntryWithRequestID(ctx)
	entry.Infof("All keys blocked for model %s, trying fallback model %s", originalModel, fallbackModel)

	return m.executeWithFallback(ctx, providers, fallbackReq, opts, visited, depth+1)
}

// executeInternal performs the core execution logic without fallback.
func (m *Manager) executeInternal(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	// No longer rotate providers - we now round-robin across all keys directly.
	// Pass providers list for fallback but pickNext will select across all.

	retryTimes, maxWait := m.retrySettings()
	attempts := retryTimes + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		// Execute with cross-provider key selection
		resp, errExec := m.executeWithProvider(ctx, normalized[0], req, opts)
		if errExec == nil {
			return resp, nil
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterError(errExec, attempt, attempts, normalized, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteCount performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins across ALL keys
// regardless of provider, ensuring fair distribution proportional to key count.
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	// No longer rotate providers - we now round-robin across all keys directly.

	retryTimes, maxWait := m.retrySettings()
	attempts := retryTimes + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		// Execute with cross-provider key selection
		resp, errExec := m.executeCountWithProvider(ctx, normalized[0], req, opts)
		if errExec == nil {
			return resp, nil
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterError(errExec, attempt, attempts, normalized, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteStream performs a streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins across ALL keys
// regardless of provider, ensuring fair distribution proportional to key count.
// When all keys for a model are blocked (modelCooldownError), it tries fallback models.
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	return m.executeStreamWithFallback(ctx, providers, req, opts, make(map[string]bool), 0)
}

// executeStreamWithFallback performs streaming execution with fallback model support.
// visited tracks models already tried to prevent infinite loops.
// depth tracks recursion depth to enforce maximum 20 fallback attempts.
func (m *Manager) executeStreamWithFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, visited map[string]bool, depth int) (<-chan cliproxyexecutor.StreamChunk, error) {
	const maxFallbackDepth = 20

	// Mark current model as visited
	originalModel := req.Model
	visited[originalModel] = true

	// Try execution with current model
	chunks, err := m.executeStreamInternal(ctx, providers, req, opts)
	if err == nil {
		return chunks, nil
	}

	// Check if this is a modelCooldownError and we should try fallback
	_, isCooldown := err.(*modelCooldownError)
	if !isCooldown {
		return chunks, err
	}

	// Check depth limit
	if depth >= maxFallbackDepth {
		return chunks, err
	}

	// Get fallback model
	fallbackModel := m.getFallbackModel(originalModel, visited)
	if fallbackModel == "" {
		return chunks, err
	}

	// Try fallback model
	fallbackReq := req
	fallbackReq.Model = fallbackModel

	entry := logEntryWithRequestID(ctx)
	entry.Infof("All keys blocked for model %s (stream), trying fallback model %s", originalModel, fallbackModel)

	return m.executeStreamWithFallback(ctx, providers, fallbackReq, opts, visited, depth+1)
}

// executeStreamInternal performs the core streaming execution logic without fallback.
func (m *Manager) executeStreamInternal(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	// No longer rotate providers - we now round-robin across all keys directly.

	retryTimes, maxWait := m.retrySettings()
	attempts := retryTimes + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		// Execute with cross-provider key selection
		chunks, errStream := m.executeStreamWithProvider(ctx, normalized[0], req, opts)
		if errStream == nil {
			return chunks, nil
		}
		lastErr = errStream
		wait, shouldRetry := m.shouldRetryAfterError(errStream, attempt, attempts, normalized, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait); errWait != nil {
			return nil, errWait
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

func (m *Manager) executeWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if provider == "" {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}
	routeModel := req.Model
	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, routeModel, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, errPick
		}

		entry := logEntryWithRequestID(ctx)
		// Use auth.Provider for logging since pickNext may select from any provider
		debugLogAuthSelection(entry, auth, auth.Provider, req.Model)

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execReq := req
		execReq.Model, execReq.Metadata = rewriteModelForAuth(routeModel, req.Metadata, auth)

		// First attempt: use primary model from round-robin (cursor increments).
		primaryModel, primaryMeta := m.applyOAuthModelMapping(auth, execReq.Model, execReq.Metadata)
		execReq.Model = primaryModel
		execReq.Metadata = primaryMeta

		resp, errExec := executor.Execute(execCtx, auth, execReq, opts)
		// Use auth.Provider for result tracking since we may have selected from a different provider
		result := Result{AuthID: auth.ID, Provider: auth.Provider, Model: routeModel, Success: errExec == nil}
		if errExec != nil {
			result.Error = &Error{Message: errExec.Error()}
			var se cliproxyexecutor.StatusError
			if errors.As(errExec, &se) && se != nil {
				result.Error.HTTPStatus = se.StatusCode()
			}
			if ra := retryAfterFromError(errExec); ra != nil {
				result.RetryAfter = ra
			}
			m.MarkResult(execCtx, result)

			// Fallback: try remaining upstream models for this alias (if any).
			aliasModel := execReq.Model
			if originalModel, ok := execReq.Metadata[util.ModelMappingOriginalModelMetadataKey].(string); ok && originalModel != "" {
				aliasModel = originalModel
			} else {
				// No mapping was applied, continue with next auth.
				lastErr = errExec
				continue
			}

			remainingModels := m.GetRemainingUpstreamModels(auth, aliasModel)
			if len(remainingModels) == 0 {
				// No fallback models, continue with next auth.
				lastErr = errExec
				continue
			}

			// Track all tried models for unified error message.
			triedModels := []string{primaryModel}
			var fallbackErr error = errExec

			for _, fallbackModel := range remainingModels {
				triedModels = append(triedModels, fallbackModel)
				fallbackReq := req
				fallbackReq.Model, fallbackReq.Metadata = rewriteModelForAuth(routeModel, req.Metadata, auth)
				fallbackReq.Model = fallbackModel
				// Preserve the original alias in metadata for response translation.
				if fallbackReq.Metadata == nil {
					fallbackReq.Metadata = make(map[string]any)
				} else {
					newMeta := make(map[string]any, len(fallbackReq.Metadata)+1)
					for k, v := range fallbackReq.Metadata {
						newMeta[k] = v
					}
					fallbackReq.Metadata = newMeta
				}
				fallbackReq.Metadata[util.ModelMappingOriginalModelMetadataKey] = aliasModel

				fallbackResp, fallbackExecErr := executor.Execute(execCtx, auth, fallbackReq, opts)
				fallbackResult := Result{AuthID: auth.ID, Provider: auth.Provider, Model: routeModel, Success: fallbackExecErr == nil}
				if fallbackExecErr != nil {
					fallbackResult.Error = &Error{Message: fallbackExecErr.Error()}
					var fallbackSE cliproxyexecutor.StatusError
					if errors.As(fallbackExecErr, &fallbackSE) && fallbackSE != nil {
						fallbackResult.Error.HTTPStatus = fallbackSE.StatusCode()
					}
					if ra := retryAfterFromError(fallbackExecErr); ra != nil {
						fallbackResult.RetryAfter = ra
					}
					m.MarkResult(execCtx, fallbackResult)
					fallbackErr = fallbackExecErr
					continue // Try next fallback model.
				}
				// Fallback succeeded.
				m.MarkResult(execCtx, fallbackResult)
				return fallbackResp, nil
			}

			// All models failed for this alias - return unified error.
			lastErr = &Error{
				Code:    "all_upstream_models_failed",
				Message: fmt.Sprintf("All upstream models failed for alias '%s': %s", aliasModel, strings.Join(triedModels, ", ")),
			}
			// Preserve HTTP status from last error if available.
			if fallbackErr != nil {
				var se cliproxyexecutor.StatusError
				if errors.As(fallbackErr, &se) && se != nil {
					lastErr.(*Error).HTTPStatus = se.StatusCode()
				}
			}
			continue
		}
		m.MarkResult(execCtx, result)
		return resp, nil
	}
}

func (m *Manager) executeCountWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if provider == "" {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}
	routeModel := req.Model
	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, routeModel, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, errPick
		}

		entry := logEntryWithRequestID(ctx)
		// Use auth.Provider for logging since pickNext may select from any provider
		debugLogAuthSelection(entry, auth, auth.Provider, req.Model)

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execReq := req
		execReq.Model, execReq.Metadata = rewriteModelForAuth(routeModel, req.Metadata, auth)

		// First attempt: use primary model from round-robin (cursor increments).
		primaryModel, primaryMeta := m.applyOAuthModelMapping(auth, execReq.Model, execReq.Metadata)
		execReq.Model = primaryModel
		execReq.Metadata = primaryMeta

		resp, errExec := executor.CountTokens(execCtx, auth, execReq, opts)
		// Use auth.Provider for result tracking since we may have selected from a different provider
		result := Result{AuthID: auth.ID, Provider: auth.Provider, Model: routeModel, Success: errExec == nil}
		if errExec != nil {
			result.Error = &Error{Message: errExec.Error()}
			var se cliproxyexecutor.StatusError
			if errors.As(errExec, &se) && se != nil {
				result.Error.HTTPStatus = se.StatusCode()
			}
			if ra := retryAfterFromError(errExec); ra != nil {
				result.RetryAfter = ra
			}
			m.MarkResult(execCtx, result)

			// Fallback: try remaining upstream models for this alias (if any).
			aliasModel := execReq.Model
			if originalModel, ok := execReq.Metadata[util.ModelMappingOriginalModelMetadataKey].(string); ok && originalModel != "" {
				aliasModel = originalModel
			} else {
				// No mapping was applied, continue with next auth.
				lastErr = errExec
				continue
			}

			remainingModels := m.GetRemainingUpstreamModels(auth, aliasModel)
			if len(remainingModels) == 0 {
				// No fallback models, continue with next auth.
				lastErr = errExec
				continue
			}

			// Track all tried models for unified error message.
			triedModels := []string{primaryModel}
			var fallbackErr error = errExec

			for _, fallbackModel := range remainingModels {
				triedModels = append(triedModels, fallbackModel)
				fallbackReq := req
				fallbackReq.Model, fallbackReq.Metadata = rewriteModelForAuth(routeModel, req.Metadata, auth)
				fallbackReq.Model = fallbackModel
				// Preserve the original alias in metadata for response translation.
				if fallbackReq.Metadata == nil {
					fallbackReq.Metadata = make(map[string]any)
				} else {
					newMeta := make(map[string]any, len(fallbackReq.Metadata)+1)
					for k, v := range fallbackReq.Metadata {
						newMeta[k] = v
					}
					fallbackReq.Metadata = newMeta
				}
				fallbackReq.Metadata[util.ModelMappingOriginalModelMetadataKey] = aliasModel

				fallbackResp, fallbackExecErr := executor.CountTokens(execCtx, auth, fallbackReq, opts)
				fallbackResult := Result{AuthID: auth.ID, Provider: auth.Provider, Model: routeModel, Success: fallbackExecErr == nil}
				if fallbackExecErr != nil {
					fallbackResult.Error = &Error{Message: fallbackExecErr.Error()}
					var fallbackSE cliproxyexecutor.StatusError
					if errors.As(fallbackExecErr, &fallbackSE) && fallbackSE != nil {
						fallbackResult.Error.HTTPStatus = fallbackSE.StatusCode()
					}
					if ra := retryAfterFromError(fallbackExecErr); ra != nil {
						fallbackResult.RetryAfter = ra
					}
					m.MarkResult(execCtx, fallbackResult)
					fallbackErr = fallbackExecErr
					continue // Try next fallback model.
				}
				// Fallback succeeded.
				m.MarkResult(execCtx, fallbackResult)
				return fallbackResp, nil
			}

			// All models failed for this alias - return unified error.
			lastErr = &Error{
				Code:    "all_upstream_models_failed",
				Message: fmt.Sprintf("All upstream models failed for alias '%s': %s", aliasModel, strings.Join(triedModels, ", ")),
			}
			// Preserve HTTP status from last error if available.
			if fallbackErr != nil {
				var se cliproxyexecutor.StatusError
				if errors.As(fallbackErr, &se) && se != nil {
					lastErr.(*Error).HTTPStatus = se.StatusCode()
				}
			}
			continue
		}
		m.MarkResult(execCtx, result)
		return resp, nil
	}
}

func (m *Manager) executeStreamWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	if provider == "" {
		return nil, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}
	routeModel := req.Model
	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, routeModel, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, errPick
		}

		entry := logEntryWithRequestID(ctx)
		// Use auth.Provider for logging since pickNext may select from any provider
		debugLogAuthSelection(entry, auth, auth.Provider, req.Model)

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execReq := req
		execReq.Model, execReq.Metadata = rewriteModelForAuth(routeModel, req.Metadata, auth)

		// First attempt: use primary model from round-robin (cursor increments).
		primaryModel, primaryMeta := m.applyOAuthModelMapping(auth, execReq.Model, execReq.Metadata)
		execReq.Model = primaryModel
		execReq.Metadata = primaryMeta

		chunks, errStream := executor.ExecuteStream(execCtx, auth, execReq, opts)
		if errStream != nil {
			rerr := &Error{Message: errStream.Error()}
			var se cliproxyexecutor.StatusError
			if errors.As(errStream, &se) && se != nil {
				rerr.HTTPStatus = se.StatusCode()
			}
			// Use auth.Provider for result tracking
			result := Result{AuthID: auth.ID, Provider: auth.Provider, Model: routeModel, Success: false, Error: rerr}
			result.RetryAfter = retryAfterFromError(errStream)
			m.MarkResult(execCtx, result)

			// Fallback: try remaining upstream models for this alias (if any).
			aliasModel := execReq.Model
			if originalModel, ok := execReq.Metadata[util.ModelMappingOriginalModelMetadataKey].(string); ok && originalModel != "" {
				aliasModel = originalModel
			} else {
				// No mapping was applied, continue with next auth.
				lastErr = errStream
				continue
			}

			remainingModels := m.GetRemainingUpstreamModels(auth, aliasModel)
			if len(remainingModels) == 0 {
				// No fallback models, continue with next auth.
				lastErr = errStream
				continue
			}

			// Track all tried models for unified error message.
			triedModels := []string{primaryModel}
			var fallbackErr error = errStream

			for _, fallbackModel := range remainingModels {
				triedModels = append(triedModels, fallbackModel)
				fallbackReq := req
				fallbackReq.Model, fallbackReq.Metadata = rewriteModelForAuth(routeModel, req.Metadata, auth)
				fallbackReq.Model = fallbackModel
				// Preserve the original alias in metadata for response translation.
				if fallbackReq.Metadata == nil {
					fallbackReq.Metadata = make(map[string]any)
				} else {
					newMeta := make(map[string]any, len(fallbackReq.Metadata)+1)
					for k, v := range fallbackReq.Metadata {
						newMeta[k] = v
					}
					fallbackReq.Metadata = newMeta
				}
				fallbackReq.Metadata[util.ModelMappingOriginalModelMetadataKey] = aliasModel

				fallbackChunks, fallbackStreamErr := executor.ExecuteStream(execCtx, auth, fallbackReq, opts)
				if fallbackStreamErr != nil {
					fallbackRerr := &Error{Message: fallbackStreamErr.Error()}
					var fallbackSE cliproxyexecutor.StatusError
					if errors.As(fallbackStreamErr, &fallbackSE) && fallbackSE != nil {
						fallbackRerr.HTTPStatus = fallbackSE.StatusCode()
					}
					fallbackResult := Result{AuthID: auth.ID, Provider: auth.Provider, Model: routeModel, Success: false, Error: fallbackRerr}
					fallbackResult.RetryAfter = retryAfterFromError(fallbackStreamErr)
					m.MarkResult(execCtx, fallbackResult)
					fallbackErr = fallbackStreamErr
					continue // Try next fallback model.
				}
				// Fallback succeeded - return the stream.
				out := make(chan cliproxyexecutor.StreamChunk)
				go func(streamCtx context.Context, streamAuth *Auth, streamChunks <-chan cliproxyexecutor.StreamChunk) {
					defer close(out)
					var failed bool
					for chunk := range streamChunks {
						if chunk.Err != nil && !failed {
							failed = true
							chunkRerr := &Error{Message: chunk.Err.Error()}
							var chunkSE cliproxyexecutor.StatusError
							if errors.As(chunk.Err, &chunkSE) && chunkSE != nil {
								chunkRerr.HTTPStatus = chunkSE.StatusCode()
							}
							m.MarkResult(streamCtx, Result{AuthID: streamAuth.ID, Provider: streamAuth.Provider, Model: routeModel, Success: false, Error: chunkRerr})
						}
						out <- chunk
					}
					if !failed {
						m.MarkResult(streamCtx, Result{AuthID: streamAuth.ID, Provider: streamAuth.Provider, Model: routeModel, Success: true})
					}
				}(execCtx, auth.Clone(), fallbackChunks)
				return out, nil
			}

			// All models failed for this alias - return unified error.
			lastErr = &Error{
				Code:    "all_upstream_models_failed",
				Message: fmt.Sprintf("All upstream models failed for alias '%s': %s", aliasModel, strings.Join(triedModels, ", ")),
			}
			// Preserve HTTP status from last error if available.
			if fallbackErr != nil {
				var se cliproxyexecutor.StatusError
				if errors.As(fallbackErr, &se) && se != nil {
					lastErr.(*Error).HTTPStatus = se.StatusCode()
				}
			}
			continue
		}
		out := make(chan cliproxyexecutor.StreamChunk)
		// Use auth.Provider for streaming result tracking
		go func(streamCtx context.Context, streamAuth *Auth, streamChunks <-chan cliproxyexecutor.StreamChunk) {
			defer close(out)
			var failed bool
			for chunk := range streamChunks {
				if chunk.Err != nil && !failed {
					failed = true
					rerr := &Error{Message: chunk.Err.Error()}
					var se cliproxyexecutor.StatusError
					if errors.As(chunk.Err, &se) && se != nil {
						rerr.HTTPStatus = se.StatusCode()
					}
					m.MarkResult(streamCtx, Result{AuthID: streamAuth.ID, Provider: streamAuth.Provider, Model: routeModel, Success: false, Error: rerr})
				}
				out <- chunk
			}
			if !failed {
				m.MarkResult(streamCtx, Result{AuthID: streamAuth.ID, Provider: streamAuth.Provider, Model: routeModel, Success: true})
			}
		}(execCtx, auth.Clone(), chunks)
		return out, nil
	}
}

func rewriteModelForAuth(model string, metadata map[string]any, auth *Auth) (string, map[string]any) {
	if auth == nil || model == "" {
		return model, metadata
	}
	prefix := strings.TrimSpace(auth.Prefix)
	if prefix == "" {
		return model, metadata
	}
	needle := prefix + "/"
	if !strings.HasPrefix(model, needle) {
		return model, metadata
	}
	rewritten := strings.TrimPrefix(model, needle)
	return rewritten, stripPrefixFromMetadata(metadata, needle)
}

func stripPrefixFromMetadata(metadata map[string]any, needle string) map[string]any {
	if len(metadata) == 0 || needle == "" {
		return metadata
	}
	keys := []string{
		util.ThinkingOriginalModelMetadataKey,
		util.GeminiOriginalModelMetadataKey,
		util.ModelMappingOriginalModelMetadataKey,
	}
	var out map[string]any
	for _, key := range keys {
		raw, ok := metadata[key]
		if !ok {
			continue
		}
		value, okStr := raw.(string)
		if !okStr || !strings.HasPrefix(value, needle) {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(metadata))
			for k, v := range metadata {
				out[k] = v
			}
		}
		out[key] = strings.TrimPrefix(value, needle)
	}
	if out == nil {
		return metadata
	}
	return out
}

func (m *Manager) normalizeProviders(providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	result := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := strings.TrimSpace(strings.ToLower(provider))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		result = append(result, p)
	}
	return result
}

// rotateProviders returns a rotated view of the providers list starting from the
// current offset for the model, and atomically increments the offset for the next call.
// This ensures concurrent requests get different starting providers.
func (m *Manager) rotateProviders(model string, providers []string) []string {
	if len(providers) == 0 {
		return nil
	}

	// Atomic read-and-increment: get current offset and advance cursor in one lock
	m.mu.Lock()
	offset := m.providerOffsets[model]
	m.providerOffsets[model] = (offset + 1) % len(providers)
	m.mu.Unlock()

	if len(providers) > 0 {
		offset %= len(providers)
	}
	if offset < 0 {
		offset = 0
	}
	if offset == 0 {
		return providers
	}
	rotated := make([]string, 0, len(providers))
	rotated = append(rotated, providers[offset:]...)
	rotated = append(rotated, providers[:offset]...)
	return rotated
}

func (m *Manager) retrySettings() (int, time.Duration) {
	if m == nil {
		return 0, 0
	}
	return int(m.requestRetry.Load()), time.Duration(m.maxRetryInterval.Load())
}

func (m *Manager) closestCooldownWait(providers []string, model string) (time.Duration, bool) {
	if m == nil || len(providers) == 0 {
		return 0, false
	}
	now := time.Now()
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.TrimSpace(strings.ToLower(providers[i]))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var (
		found   bool
		minWait time.Duration
	)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		blocked, reason, next := isAuthBlockedForModel(auth, model, now)
		if !blocked || next.IsZero() || reason == blockReasonDisabled {
			continue
		}
		wait := next.Sub(now)
		if wait < 0 {
			continue
		}
		if !found || wait < minWait {
			minWait = wait
			found = true
		}
	}
	return minWait, found
}

func (m *Manager) shouldRetryAfterError(err error, attempt, maxAttempts int, providers []string, model string, maxWait time.Duration) (time.Duration, bool) {
	if err == nil || attempt >= maxAttempts-1 {
		return 0, false
	}
	if maxWait <= 0 {
		return 0, false
	}
	if status := statusCodeFromError(err); status == http.StatusOK {
		return 0, false
	}
	wait, found := m.closestCooldownWait(providers, model)
	if !found || wait > maxWait {
		return 0, false
	}
	return wait, true
}

func waitForCooldown(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *Manager) executeProvidersOnce(ctx context.Context, providers []string, fn func(context.Context, string) (cliproxyexecutor.Response, error)) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	var lastErr error
	for _, provider := range providers {
		resp, errExec := fn(ctx, provider)
		if errExec == nil {
			return resp, nil
		}
		lastErr = errExec
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

func (m *Manager) executeStreamProvidersOnce(ctx context.Context, providers []string, fn func(context.Context, string) (<-chan cliproxyexecutor.StreamChunk, error)) (<-chan cliproxyexecutor.StreamChunk, error) {
	if len(providers) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	var lastErr error
	for _, provider := range providers {
		chunks, errExec := fn(ctx, provider)
		if errExec == nil {
			return chunks, nil
		}
		lastErr = errExec
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// MarkResult records an execution result and notifies hooks.
func (m *Manager) MarkResult(ctx context.Context, result Result) {
	if result.AuthID == "" {
		return
	}

	shouldResumeModel := false
	shouldSuspendModel := false
	suspendReason := ""
	clearModelQuota := false
	setModelQuota := false

	m.mu.Lock()
	if auth, ok := m.auths[result.AuthID]; ok && auth != nil {
		now := time.Now()

		if result.Success {
			if result.Model != "" {
				state := ensureModelState(auth, result.Model)
				resetModelState(state, now)
				updateAggregatedAvailability(auth, now)
				if !hasModelError(auth, now) {
					auth.LastError = nil
					auth.StatusMessage = ""
					auth.Status = StatusActive
				}
				auth.UpdatedAt = now
				shouldResumeModel = true
				clearModelQuota = true
			} else {
				clearAuthStateOnSuccess(auth, now)
			}
		} else {
			if result.Model != "" {
				state := ensureModelState(auth, result.Model)
				state.Unavailable = true
				state.Status = StatusError
				state.UpdatedAt = now
				if result.Error != nil {
					state.LastError = cloneError(result.Error)
					state.StatusMessage = result.Error.Message
					auth.LastError = cloneError(result.Error)
					auth.StatusMessage = result.Error.Message
				}

				statusCode := statusCodeFromResult(result.Error)
				switch statusCode {
				case 401:
					next := now.Add(2 * time.Hour)
					state.NextRetryAfter = next
					suspendReason = "unauthorized"
					shouldSuspendModel = true
				case 402, 403:
					next := now.Add(2 * time.Hour)
					state.NextRetryAfter = next
					suspendReason = "payment_required"
					shouldSuspendModel = true
				case 404:
					next := now.Add(12 * time.Hour)
					state.NextRetryAfter = next
					suspendReason = "not_found"
					shouldSuspendModel = true
				case 429:
					var next time.Time
					backoffLevel := state.Quota.BackoffLevel
					if result.RetryAfter != nil {
						next = now.Add(*result.RetryAfter)
					} else {
						cooldown, nextLevel := nextQuotaCooldown(backoffLevel)
						if cooldown > 0 {
							next = now.Add(cooldown)
						}
						backoffLevel = nextLevel
					}
					state.NextRetryAfter = next
					state.Quota = QuotaState{
						Exceeded:      true,
						Reason:        "quota",
						NextRecoverAt: next,
						BackoffLevel:  backoffLevel,
					}
					suspendReason = "quota"
					shouldSuspendModel = true
					setModelQuota = true
				case 408, 500, 502, 503, 504:
					next := now.Add(2 * time.Hour)
					state.NextRetryAfter = next
					suspendReason = "server_error"
					shouldSuspendModel = true
				default:
					next := now.Add(2 * time.Hour)
					state.NextRetryAfter = next
					suspendReason = "generic_error"
					shouldSuspendModel = true
				}

				auth.Status = StatusError
				auth.UpdatedAt = now
				updateAggregatedAvailability(auth, now)
			} else {
				applyAuthFailureState(auth, result.Error, result.RetryAfter, now)
			}
		}

		_ = m.persist(ctx, auth)
	}
	m.mu.Unlock()

	if clearModelQuota && result.Model != "" {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(result.AuthID, result.Model)
	}
	if setModelQuota && result.Model != "" {
		registry.GetGlobalRegistry().SetModelQuotaExceeded(result.AuthID, result.Model)
	}
	if shouldResumeModel {
		registry.GetGlobalRegistry().ResumeClientModel(result.AuthID, result.Model)
	} else if shouldSuspendModel {
		registry.GetGlobalRegistry().SuspendClientModel(result.AuthID, result.Model, suspendReason)
	}

	m.hook.OnResult(ctx, result)
}

func ensureModelState(auth *Auth, model string) *ModelState {
	if auth == nil || model == "" {
		return nil
	}
	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*ModelState)
	}
	if state, ok := auth.ModelStates[model]; ok && state != nil {
		return state
	}
	state := &ModelState{Status: StatusActive}
	auth.ModelStates[model] = state
	return state
}

func resetModelState(state *ModelState, now time.Time) {
	if state == nil {
		return
	}
	state.Unavailable = false
	state.Status = StatusActive
	state.StatusMessage = ""
	state.NextRetryAfter = time.Time{}
	state.LastError = nil
	state.Quota = QuotaState{}
	state.UpdatedAt = now
}

func updateAggregatedAvailability(auth *Auth, now time.Time) {
	if auth == nil || len(auth.ModelStates) == 0 {
		return
	}
	allUnavailable := true
	earliestRetry := time.Time{}
	quotaExceeded := false
	quotaRecover := time.Time{}
	maxBackoffLevel := 0
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		stateUnavailable := false
		if state.Status == StatusDisabled {
			stateUnavailable = true
		} else if state.Unavailable {
			if state.NextRetryAfter.IsZero() {
				stateUnavailable = true
			} else if state.NextRetryAfter.After(now) {
				stateUnavailable = true
				if earliestRetry.IsZero() || state.NextRetryAfter.Before(earliestRetry) {
					earliestRetry = state.NextRetryAfter
				}
			} else {
				state.Unavailable = false
				state.NextRetryAfter = time.Time{}
			}
		}
		if !stateUnavailable {
			allUnavailable = false
		}
		if state.Quota.Exceeded {
			quotaExceeded = true
			if quotaRecover.IsZero() || (!state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.Before(quotaRecover)) {
				quotaRecover = state.Quota.NextRecoverAt
			}
			if state.Quota.BackoffLevel > maxBackoffLevel {
				maxBackoffLevel = state.Quota.BackoffLevel
			}
		}
	}
	auth.Unavailable = allUnavailable
	if allUnavailable {
		auth.NextRetryAfter = earliestRetry
	} else {
		auth.NextRetryAfter = time.Time{}
	}
	if quotaExceeded {
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		auth.Quota.NextRecoverAt = quotaRecover
		auth.Quota.BackoffLevel = maxBackoffLevel
	} else {
		auth.Quota.Exceeded = false
		auth.Quota.Reason = ""
		auth.Quota.NextRecoverAt = time.Time{}
		auth.Quota.BackoffLevel = 0
	}
}

func hasModelError(auth *Auth, now time.Time) bool {
	if auth == nil || len(auth.ModelStates) == 0 {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.LastError != nil {
			return true
		}
		if state.Status == StatusError {
			if state.Unavailable && (state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(now)) {
				return true
			}
		}
	}
	return false
}

func clearAuthStateOnSuccess(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.Status = StatusActive
	auth.StatusMessage = ""
	auth.Quota.Exceeded = false
	auth.Quota.Reason = ""
	auth.Quota.NextRecoverAt = time.Time{}
	auth.Quota.BackoffLevel = 0
	auth.LastError = nil
	auth.NextRetryAfter = time.Time{}
	auth.UpdatedAt = now
}

func cloneError(err *Error) *Error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:       err.Code,
		Message:    err.Message,
		Retryable:  err.Retryable,
		HTTPStatus: err.HTTPStatus,
	}
}

func statusCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		return sc.StatusCode()
	}
	return 0
}

func retryAfterFromError(err error) *time.Duration {
	if err == nil {
		return nil
	}
	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	rap, ok := err.(retryAfterProvider)
	if !ok || rap == nil {
		return nil
	}
	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		return nil
	}
	val := *retryAfter
	return &val
}

func statusCodeFromResult(err *Error) int {
	if err == nil {
		return 0
	}
	return err.StatusCode()
}

func applyAuthFailureState(auth *Auth, resultErr *Error, retryAfter *time.Duration, now time.Time) {
	if auth == nil {
		return
	}
	auth.Unavailable = true
	auth.Status = StatusError
	auth.UpdatedAt = now
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
		if resultErr.Message != "" {
			auth.StatusMessage = resultErr.Message
		}
	}
	statusCode := statusCodeFromResult(resultErr)
	switch statusCode {
	case 401:
		auth.StatusMessage = "unauthorized"
		auth.NextRetryAfter = now.Add(2 * time.Hour)
	case 402, 403:
		auth.StatusMessage = "payment_required"
		auth.NextRetryAfter = now.Add(2 * time.Hour)
	case 404:
		auth.StatusMessage = "not_found"
		auth.NextRetryAfter = now.Add(12 * time.Hour)
	case 429:
		auth.StatusMessage = "quota exhausted"
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		var next time.Time
		if retryAfter != nil {
			next = now.Add(*retryAfter)
		} else {
			cooldown, nextLevel := nextQuotaCooldown(auth.Quota.BackoffLevel)
			if cooldown > 0 {
				next = now.Add(cooldown)
			}
			auth.Quota.BackoffLevel = nextLevel
		}
		auth.Quota.NextRecoverAt = next
		auth.NextRetryAfter = next
	case 408, 500, 502, 503, 504:
		auth.StatusMessage = "transient upstream error"
		auth.NextRetryAfter = now.Add(2 * time.Hour)
	default:
		if auth.StatusMessage == "" {
			auth.StatusMessage = "request failed"
		}
	}
}

// nextQuotaCooldown returns the next cooldown duration and updated backoff level for repeated quota errors.
func nextQuotaCooldown(prevLevel int) (time.Duration, int) {
	if prevLevel < 0 {
		prevLevel = 0
	}
	if quotaCooldownDisabled.Load() {
		return 0, prevLevel
	}
	cooldown := quotaBackoffBase * time.Duration(1<<prevLevel)
	if cooldown < quotaBackoffBase {
		cooldown = quotaBackoffBase
	}
	if cooldown >= quotaBackoffMax {
		return quotaBackoffMax, prevLevel
	}
	return cooldown, prevLevel + 1
}

// List returns all auth entries currently known by the manager.
func (m *Manager) List() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		list = append(list, auth.Clone())
	}
	return list
}

// GetByID retrieves an auth entry by its ID.

func (m *Manager) GetByID(id string) (*Auth, bool) {
	if id == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth, ok := m.auths[id]
	if !ok {
		return nil, false
	}
	return auth.Clone(), true
}

// ListByProvider returns all auth entries for the specified provider.
// Only active (non-disabled) entries are returned.
func (m *Manager) ListByProvider(provider string) []*Auth {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Auth, 0)
	for _, auth := range m.auths {
		if auth.Disabled {
			continue
		}
		if strings.ToLower(auth.Provider) == provider {
			list = append(list, auth.Clone())
		}
	}
	return list
}

// ExecuteWithAuth executes a request using a specific auth credential directly.
// This bypasses the normal selector/round-robin logic and uses the specified auth.
// Primarily used for warmup operations where each credential must be exercised individually.
func (m *Manager) ExecuteWithAuth(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if auth == nil {
		return cliproxyexecutor.Response{}, &Error{Code: "auth_required", Message: "auth cannot be nil"}
	}

	m.mu.RLock()
	executor, ok := m.executors[auth.Provider]
	m.mu.RUnlock()

	if !ok {
		return cliproxyexecutor.Response{}, &Error{Code: "executor_not_found", Message: "executor not registered for provider: " + auth.Provider}
	}

	execCtx := ctx
	if rt := m.roundTripperFor(auth); rt != nil {
		execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
		execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
	}

	resp, err := executor.Execute(execCtx, auth, req, opts)

	// Mark result for quota tracking
	result := Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    req.Model,
		Success:  err == nil,
	}
	if err != nil {
		result.Error = &Error{Message: err.Error()}
	}
	m.MarkResult(execCtx, result)

	return resp, err
}

func (m *Manager) pickNext(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	m.mu.RLock()
	// Collect ALL candidates from ALL providers that support this model.
	// This enables true cross-provider round-robin by key count.
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	registryRef := registry.GetGlobalRegistry()
	for _, candidate := range m.auths {
		if candidate.Disabled {
			continue
		}
		// Check if this auth's provider has a registered executor
		if _, hasExecutor := m.executors[candidate.Provider]; !hasExecutor {
			continue
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		if modelKey != "" && registryRef != nil && !registryRef.ClientSupportsModel(candidate.ID, modelKey) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	// Pass empty provider to selector since we're doing cross-provider selection.
	// The selector will round-robin based on model only.
	selected, errPick := m.selector.Pick(ctx, "", model, opts, candidates)
	if errPick != nil {
		m.mu.RUnlock()
		return nil, nil, errPick
	}
	if selected == nil {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	// Get the executor for the selected auth's provider
	executor, okExecutor := m.executors[selected.Provider]
	if !okExecutor {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered for provider: " + selected.Provider}
	}
	authCopy := selected.Clone()
	m.mu.RUnlock()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, nil
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil || auth == nil {
		return nil
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return nil
		}
	}
	// Skip persistence when metadata is absent (e.g., runtime-only auths).
	if auth.Metadata == nil {
		return nil
	}
	_, err := m.store.Save(ctx, auth)
	return err
}

// StartAutoRefresh launches a background loop that evaluates auth freshness
// every few seconds and triggers refresh operations when required.
// Only one loop is kept alive; starting a new one cancels the previous run.
func (m *Manager) StartAutoRefresh(parent context.Context, interval time.Duration) {
	if interval <= 0 || interval > refreshCheckInterval {
		interval = refreshCheckInterval
	} else {
		interval = refreshCheckInterval
	}
	if m.refreshCancel != nil {
		m.refreshCancel()
		m.refreshCancel = nil
	}
	ctx, cancel := context.WithCancel(parent)
	m.refreshCancel = cancel
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		m.checkRefreshes(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.checkRefreshes(ctx)
			}
		}
	}()
}

// StopAutoRefresh cancels the background refresh loop, if running.
func (m *Manager) StopAutoRefresh() {
	if m.refreshCancel != nil {
		m.refreshCancel()
		m.refreshCancel = nil
	}
}

func (m *Manager) checkRefreshes(ctx context.Context) {
	// log.Debugf("checking refreshes")
	now := time.Now()
	snapshot := m.snapshotAuths()
	for _, a := range snapshot {
		typ, _ := a.AccountInfo()
		if typ != "api_key" {
			if !m.shouldRefresh(a, now) {
				continue
			}
			log.Debugf("checking refresh for %s, %s, %s", a.Provider, a.ID, typ)

			if exec := m.executorFor(a.Provider); exec == nil {
				continue
			}
			if !m.markRefreshPending(a.ID, now) {
				continue
			}
			go m.refreshAuth(ctx, a.ID)
		}
	}
}

func (m *Manager) snapshotAuths() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Auth, 0, len(m.auths))
	for _, a := range m.auths {
		out = append(out, a.Clone())
	}
	return out
}

func (m *Manager) shouldRefresh(a *Auth, now time.Time) bool {
	if a == nil || a.Disabled {
		return false
	}
	if !a.NextRefreshAfter.IsZero() && now.Before(a.NextRefreshAfter) {
		return false
	}
	if evaluator, ok := a.Runtime.(RefreshEvaluator); ok && evaluator != nil {
		return evaluator.ShouldRefresh(now, a)
	}

	lastRefresh := a.LastRefreshedAt
	if lastRefresh.IsZero() {
		if ts, ok := authLastRefreshTimestamp(a); ok {
			lastRefresh = ts
		}
	}

	expiry, hasExpiry := a.ExpirationTime()

	if interval := authPreferredInterval(a); interval > 0 {
		if hasExpiry && !expiry.IsZero() {
			if !expiry.After(now) {
				return true
			}
			if expiry.Sub(now) <= interval {
				return true
			}
		}
		if lastRefresh.IsZero() {
			return true
		}
		return now.Sub(lastRefresh) >= interval
	}

	provider := strings.ToLower(a.Provider)
	lead := ProviderRefreshLead(provider, a.Runtime)
	if lead == nil {
		return false
	}
	if *lead <= 0 {
		if hasExpiry && !expiry.IsZero() {
			return now.After(expiry)
		}
		return false
	}
	if hasExpiry && !expiry.IsZero() {
		return time.Until(expiry) <= *lead
	}
	if !lastRefresh.IsZero() {
		return now.Sub(lastRefresh) >= *lead
	}
	return true
}

func authPreferredInterval(a *Auth) time.Duration {
	if a == nil {
		return 0
	}
	if d := durationFromMetadata(a.Metadata, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	if d := durationFromAttributes(a.Attributes, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	return 0
}

func durationFromMetadata(meta map[string]any, keys ...string) time.Duration {
	if len(meta) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if dur := parseDurationValue(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func durationFromAttributes(attrs map[string]string, keys ...string) time.Duration {
	if len(attrs) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := attrs[key]; ok {
			if dur := parseDurationString(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func parseDurationValue(val any) time.Duration {
	switch v := val.(type) {
	case time.Duration:
		if v <= 0 {
			return 0
		}
		return v
	case int:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int32:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint32:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint64:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case float32:
		if v <= 0 {
			return 0
		}
		return time.Duration(float64(v) * float64(time.Second))
	case float64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v * float64(time.Second))
	case json.Number:
		if i, err := v.Int64(); err == nil {
			if i <= 0 {
				return 0
			}
			return time.Duration(i) * time.Second
		}
		if f, err := v.Float64(); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	case string:
		return parseDurationString(v)
	}
	return 0
}

func parseDurationString(raw string) time.Duration {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if dur, err := time.ParseDuration(s); err == nil && dur > 0 {
		return dur
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func authLastRefreshTimestamp(a *Auth) (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if a.Metadata != nil {
		if ts, ok := lookupMetadataTime(a.Metadata, "last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"); ok {
			return ts, true
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"} {
			if val := strings.TrimSpace(a.Attributes[key]); val != "" {
				if ts, ok := parseTimeValue(val); ok {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

func lookupMetadataTime(meta map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseTimeValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func (m *Manager) markRefreshPending(id string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	auth, ok := m.auths[id]
	if !ok || auth == nil || auth.Disabled {
		return false
	}
	if !auth.NextRefreshAfter.IsZero() && now.Before(auth.NextRefreshAfter) {
		return false
	}
	auth.NextRefreshAfter = now.Add(refreshPendingBackoff)
	m.auths[id] = auth
	return true
}

func (m *Manager) refreshAuth(ctx context.Context, id string) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	auth := m.auths[id]
	var exec ProviderExecutor
	if auth != nil {
		exec = m.executors[auth.Provider]
	}
	m.mu.RUnlock()
	if auth == nil || exec == nil {
		return
	}
	cloned := auth.Clone()
	updated, err := exec.Refresh(ctx, cloned)
	if err != nil && errors.Is(err, context.Canceled) {
		log.Debugf("refresh canceled for %s, %s", auth.Provider, auth.ID)
		return
	}
	log.Debugf("refreshed %s, %s, %v", auth.Provider, auth.ID, err)
	now := time.Now()
	if err != nil {
		m.mu.Lock()
		if current := m.auths[id]; current != nil {
			current.NextRefreshAfter = now.Add(refreshFailureBackoff)
			current.LastError = &Error{Message: err.Error()}
			m.auths[id] = current
		}
		m.mu.Unlock()
		return
	}
	if updated == nil {
		updated = cloned
	}
	// Preserve runtime created by the executor during Refresh.
	// If executor didn't set one, fall back to the previous runtime.
	if updated.Runtime == nil {
		updated.Runtime = auth.Runtime
	}
	updated.LastRefreshedAt = now
	// Preserve NextRefreshAfter set by the Authenticator
	// If the Authenticator set a reasonable refresh time, it should not be overwritten
	// If the Authenticator did not set it (zero value), shouldRefresh will use default logic
	updated.LastError = nil
	updated.UpdatedAt = now
	_, _ = m.Update(ctx, updated)
}

func (m *Manager) executorFor(provider string) ProviderExecutor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executors[provider]
}

// roundTripperContextKey is an unexported context key type to avoid collisions.
type roundTripperContextKey struct{}

// roundTripperFor retrieves an HTTP RoundTripper for the given auth if a provider is registered.
func (m *Manager) roundTripperFor(auth *Auth) http.RoundTripper {
	m.mu.RLock()
	p := m.rtProvider
	m.mu.RUnlock()
	if p == nil || auth == nil {
		return nil
	}
	return p.RoundTripperFor(auth)
}

// RoundTripperProvider defines a minimal provider of per-auth HTTP transports.
type RoundTripperProvider interface {
	RoundTripperFor(auth *Auth) http.RoundTripper
}

// RequestPreparer is an optional interface that provider executors can implement
// to mutate outbound HTTP requests with provider credentials.
type RequestPreparer interface {
	PrepareRequest(req *http.Request, auth *Auth) error
}

func executorKeyFromAuth(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		providerKey := strings.TrimSpace(auth.Attributes["provider_key"])
		compatName := strings.TrimSpace(auth.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return strings.ToLower(providerKey)
		}
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

// logEntryWithRequestID returns a logrus entry with request_id field if available in context.
func logEntryWithRequestID(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

func debugLogAuthSelection(entry *log.Entry, auth *Auth, provider string, model string) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	if entry == nil || auth == nil {
		return
	}
	accountType, accountInfo := auth.AccountInfo()
	proxyInfo := auth.ProxyInfo()
	suffix := ""
	if proxyInfo != "" {
		suffix = " " + proxyInfo
	}
	switch accountType {
	case "api_key":
		entry.Debugf("Use API key %s for model %s%s", util.HideAPIKey(accountInfo), model, suffix)
	case "oauth":
		ident := formatOauthIdentity(auth, provider, accountInfo)
		entry.Debugf("Use OAuth %s for model %s%s", ident, model, suffix)
	}
}

func formatOauthIdentity(auth *Auth, provider string, accountInfo string) string {
	if auth == nil {
		return ""
	}
	// Prefer the auth's provider when available.
	providerName := strings.TrimSpace(auth.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(provider)
	}
	// Only log the basename to avoid leaking host paths.
	// FileName may be unset for some auth backends; fall back to ID.
	authFile := strings.TrimSpace(auth.FileName)
	if authFile == "" {
		authFile = strings.TrimSpace(auth.ID)
	}
	if authFile != "" {
		authFile = filepath.Base(authFile)
	}
	parts := make([]string, 0, 3)
	if providerName != "" {
		parts = append(parts, "provider="+providerName)
	}
	if authFile != "" {
		parts = append(parts, "auth_file="+authFile)
	}
	if len(parts) == 0 {
		return accountInfo
	}
	return strings.Join(parts, " ")
}

// InjectCredentials delegates per-provider HTTP request preparation when supported.
// If the registered executor for the auth provider implements RequestPreparer,
// it will be invoked to modify the request (e.g., add headers).
func (m *Manager) InjectCredentials(req *http.Request, authID string) error {
	if req == nil || authID == "" {
		return nil
	}
	m.mu.RLock()
	a := m.auths[authID]
	var exec ProviderExecutor
	if a != nil {
		exec = m.executors[executorKeyFromAuth(a)]
	}
	m.mu.RUnlock()
	if a == nil || exec == nil {
		return nil
	}
	if p, ok := exec.(RequestPreparer); ok && p != nil {
		return p.PrepareRequest(req, a)
	}
	return nil
}

// PrepareHttpRequest injects provider credentials into the supplied HTTP request.
func (m *Manager) PrepareHttpRequest(ctx context.Context, auth *Auth, req *http.Request) error {
	if m == nil {
		return &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	if ctx != nil {
		*req = *req.WithContext(ctx)
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	preparer, ok := exec.(RequestPreparer)
	if !ok || preparer == nil {
		return &Error{Code: "not_supported", Message: "executor does not support http request preparation"}
	}
	return preparer.PrepareRequest(req, auth)
}

// NewHttpRequest constructs a new HTTP request and injects provider credentials into it.
func (m *Manager) NewHttpRequest(ctx context.Context, auth *Auth, method, targetURL string, body []byte, headers http.Header) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	method = strings.TrimSpace(method)
	if method == "" {
		method = http.MethodGet
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		httpReq.Header = headers.Clone()
	}
	if errPrepare := m.PrepareHttpRequest(ctx, auth, httpReq); errPrepare != nil {
		return nil, errPrepare
	}
	return httpReq, nil
}

// HttpRequest injects provider credentials into the supplied HTTP request and executes it.
func (m *Manager) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	if m == nil {
		return nil, &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return nil, &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return nil, &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return nil, &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return nil, &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	return exec.HttpRequest(ctx, auth, req)
}

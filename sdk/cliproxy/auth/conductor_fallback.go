package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// GetSelector returns the active credential selector.
func (m *Manager) GetSelector() Selector {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.selector
}

func (m *Manager) SetFallbackModels(models map[string]string) {
	if m == nil {
		return
	}
	if models == nil {
		models = map[string]string{}
	}
	m.fallbackModels.Store(models)
}

func (m *Manager) getFallbackModel(originalModel string) (string, bool) {
	if m == nil {
		return "", false
	}
	models, ok := m.fallbackModels.Load().(map[string]string)
	if !ok || models == nil {
		return "", false
	}
	fallback, exists := models[originalModel]
	return fallback, exists && fallback != ""
}

func (m *Manager) SetFallbackChain(chain []string, maxDepth int) {
	if m == nil {
		return
	}
	if chain == nil {
		chain = []string{}
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	m.fallbackChain.Store(chain)
	m.fallbackMaxDepth.Store(int32(maxDepth))
}

func (m *Manager) getFallbackChain() []string {
	if m == nil {
		return nil
	}
	chain, ok := m.fallbackChain.Load().([]string)
	if !ok {
		return nil
	}
	return chain
}

// FallbackChain returns the current fallback chain for logging and diagnostics.
func (m *Manager) FallbackChain() []string {
	return m.getFallbackChain()
}

// FallbackModels returns the current fallback-model mapping for logging and diagnostics.
func (m *Manager) FallbackModels() map[string]string {
	if m == nil {
		return nil
	}
	models, _ := m.fallbackModels.Load().(map[string]string)
	return models
}

func (m *Manager) getFallbackMaxDepth() int {
	if m == nil {
		return 3
	}
	depth := m.fallbackMaxDepth.Load()
	if depth <= 0 {
		return 3
	}
	return int(depth)
}

// modelAccessPatternsFromContext recovers the authenticated API key's model
// whitelist from the request-scoped gin context. Route fallback runs deep inside
// the auth manager, long after the HTTP middleware that enforced the whitelist on
// the originally requested model, so the policy has to be re-read here.
func modelAccessPatternsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return nil
	}
	return sdkaccess.ModelAccessPatterns(ginCtx)
}

// fallbackModelAllowed reports whether a fallback target may be served to the
// caller. A downstream API key restricted to a set of models must not reach an
// unlisted model just because the requested one failed.
func fallbackModelAllowed(patterns []string, fallbackModel string) bool {
	if len(patterns) == 0 {
		return true
	}
	if sdkaccess.ModelAllowed(fallbackModel, patterns) {
		return true
	}
	// Fallback entries may be registry aliases while the whitelist names the
	// upstream model (or the reverse), so both identities are checked.
	if resolved := resolveActualModelName(fallbackModel); resolved.isAlias && resolved.actual != "" {
		return sdkaccess.ModelAllowed(resolved.actual, patterns)
	}
	return false
}

func (m *Manager) resolveFallbackModels(ctx context.Context, originalModel string) []string {
	candidates := make([]string, 0)
	seen := map[string]struct{}{originalModel: {}}
	patterns := modelAccessPatternsFromContext(ctx)
	appendCandidate := func(model string) {
		if _, duplicate := seen[model]; duplicate {
			return
		}
		seen[model] = struct{}{}
		if !fallbackModelAllowed(patterns, model) {
			logEntryWithRequestID(ctx).WithFields(log.Fields{
				"requested_model": strings.TrimSpace(originalModel),
				"fallback_model":  strings.TrimSpace(model),
			}).Debug("fallback candidate skipped: model not allowed for this API key")
			return
		}
		candidates = append(candidates, model)
	}
	if fallback, ok := m.getFallbackModel(originalModel); ok {
		appendCandidate(fallback)
	}
	for _, chainModel := range m.getFallbackChain() {
		appendCandidate(chainModel)
	}
	if maxDepth := m.getFallbackMaxDepth(); len(candidates) > maxDepth {
		candidates = candidates[:maxDepth]
	}
	return candidates
}

// ResolveProvidersForFallback returns the first fallback model with an available provider.
func (m *Manager) ResolveProvidersForFallback(ctx context.Context, originalModel string) ([]string, string) {
	if m == nil {
		return nil, ""
	}
	for _, fallbackModel := range m.resolveFallbackModels(ctx, originalModel) {
		providers := m.ProvidersForRouteModel(fallbackModel)
		if len(providers) > 0 {
			return providers, fallbackModel
		}
		providers = m.ProvidersForOAuthAliasWithoutRegisteredModels(fallbackModel)
		if len(providers) > 0 {
			return providers, fallbackModel
		}
	}
	return nil, ""
}

func (m *Manager) fallbackSourceForModel(originalModel, fallbackModel string) string {
	if fallback, ok := m.getFallbackModel(originalModel); ok && fallback == fallbackModel {
		return "fallback-models"
	}
	return "fallback-chain"
}

func (m *Manager) ProvidersForRouteModel(routeModel string) []string {
	if m == nil {
		return nil
	}
	auths := m.List()
	providers := make([]string, 0, len(auths))
	seen := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled || !m.AuthSupportsRouteModel(auth, routeModel) {
			continue
		}
		providerKey := effectiveProviderKey(auth)
		if providerKey == "" {
			continue
		}
		if _, exists := seen[providerKey]; exists {
			continue
		}
		seen[providerKey] = struct{}{}
		providers = append(providers, providerKey)
	}
	return providers
}

func (m *Manager) AuthSupportsRouteModel(auth *Auth, routeModel string) bool {
	if m == nil || auth == nil {
		return false
	}
	return m.authSupportsRouteModel(registry.GetGlobalRegistry(), auth, routeModel)
}

func (m *Manager) ProvidersForOAuthAliasWithoutRegisteredModels(routeModel string) []string {
	if m == nil {
		return nil
	}
	routeModel = strings.TrimSpace(routeModel)
	if routeModel == "" {
		return nil
	}
	reg := registry.GetGlobalRegistry()
	providers := make([]string, 0)
	seen := make(map[string]struct{})
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		providerKey := effectiveProviderKey(auth)
		if providerKey == "" {
			continue
		}
		kind, _ := auth.AccountInfo()
		if kind == "" && auth.Attributes != nil {
			kind = strings.TrimSpace(auth.Attributes["auth_kind"])
		}
		if strings.EqualFold(strings.TrimSpace(kind), "api_key") || strings.EqualFold(strings.TrimSpace(kind), "apikey") {
			continue
		}
		if strings.TrimSpace(modelAliasChannel(auth)) == "" {
			continue
		}
		if reg != nil && len(reg.GetModelsForClient(strings.TrimSpace(auth.ID))) > 0 {
			continue
		}
		resolved := strings.TrimSpace(m.resolveOAuthUpstreamModel(auth, routeModel))
		if resolved == "" || canonicalModelKey(resolved) == canonicalModelKey(routeModel) {
			continue
		}
		resolvedBaseModel := strings.TrimSpace(thinking.ParseSuffix(resolved).ModelName)
		resolvedProviders := inferProvidersForUnregisteredOAuthAlias(resolvedBaseModel)
		if len(resolvedProviders) == 0 && resolvedBaseModel != resolved {
			resolvedProviders = inferProvidersForUnregisteredOAuthAlias(resolved)
		}
		if len(resolvedProviders) > 0 {
			matched := false
			for _, resolvedProvider := range resolvedProviders {
				if strings.EqualFold(strings.TrimSpace(resolvedProvider), providerKey) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if _, exists := seen[providerKey]; exists {
			continue
		}
		seen[providerKey] = struct{}{}
		providers = append(providers, providerKey)
	}
	return providers
}

func inferProvidersForUnregisteredOAuthAlias(modelName string) []string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil
	}
	if info := registry.LookupModelInfo(modelName); info != nil {
		if providerType := strings.ToLower(strings.TrimSpace(info.Type)); providerType != "" {
			return []string{providerType}
		}
	}
	return util.GetProviderName(modelName)
}

type resolvedModelInfo struct {
	actual  string
	isAlias bool
}

// resolveActualModelName reports whether the supplied model name is an alias
// registered through the global model registry, and if so returns the upstream
// model that the alias resolves to. The (actual, isAlias=false) result means
// the supplied name is a real upstream model rather than an alias, while the
// empty (actual, isAlias=false) result means the model is unknown.
func resolveActualModelName(modelName string) resolvedModelInfo {
	trimmed := strings.TrimSpace(modelName)
	if trimmed == "" {
		return resolvedModelInfo{}
	}
	info := registry.LookupModelInfo(trimmed)
	if info == nil {
		return resolvedModelInfo{}
	}
	if alias := strings.TrimSpace(info.Alias); alias != "" && !strings.EqualFold(alias, trimmed) {
		target := strings.TrimSpace(info.ExecutionTarget)
		if target == "" {
			target = strings.TrimSpace(info.ID)
		}
		if target == "" {
			target = alias
		}
		return resolvedModelInfo{actual: target, isAlias: true}
	}
	return resolvedModelInfo{actual: trimmed, isAlias: false}
}

func logRouteModelFallbackResult(ctx context.Context, originalModel, fallbackModel, source string, triggerErr, resultErr error, startedAt time.Time) {
	fields := log.Fields{
		"requested_model":         strings.TrimSpace(originalModel),
		"selected_fallback_model": strings.TrimSpace(fallbackModel),
		"fallback_source":         strings.TrimSpace(source),
	}
	if !startedAt.IsZero() {
		fields["elapsed_ms"] = time.Since(startedAt).Milliseconds()
	}
	if status := statusCodeFromError(triggerErr); status > 0 {
		fields["fallback_trigger_status"] = status
	}
	if triggerErr != nil {
		fields["fallback_trigger_error"] = triggerErr.Error()
	}
	provider, authID, authLabel := GetProviderAuthFromContext(ctx)
	if provider = strings.TrimSpace(provider); provider != "" {
		fields["selected_provider"] = provider
	}
	if authID = strings.TrimSpace(authID); authID != "" {
		fields["selected_auth_id"] = authID
	}
	if authLabel = strings.TrimSpace(authLabel); authLabel != "" {
		fields["selected_auth_label"] = authLabel
	}
	fallbackLabel := fmt.Sprintf("%s → %s (%s)", originalModel, fallbackModel, source)
	if resultErr != nil {
		fields["outcome"] = "error"
		if status := statusCodeFromError(resultErr); status > 0 {
			fields["fallback_result_status"] = status
		}
		fields["fallback_result_error"] = resultErr.Error()
		logEntryWithRequestID(ctx).WithFields(fields).Debugf("fallback model %s finished", fallbackLabel)
		return
	}
	fields["outcome"] = "success"
	logEntryWithRequestID(ctx).WithFields(fields).Infof("fallback model %s finished", fallbackLabel)
}

func (m *Manager) executeWithRouteFallback(
	ctx context.Context,
	providers []string,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	execOnce func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options, int, int, int) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()
	originalModel := req.Model
	attempted := map[string]struct{}{originalModel: {}}
	fallbackAllowed := m.fallbackRetryAllowedForModel(originalModel)
	// The requested model's allowlist state gates the retry loop itself (via
	// fallbackAllowed passed into executeWithRetry below), not
	// maxRetryCredentials: request-level retry rounds are controlled by
	// defaultRequestRetry independently of the per-round credential limit, so
	// zeroing maxRetryCredentials alone cannot stop a denied model from
	// retrying when request-retry is configured. The initial attempt always
	// runs with the real configured maxRetryCredentials.
	resp, lastErr := m.executeWithRetry(ctx, providers, req, opts, maxRetryCredentials, defaultRequestRetry, maxWait, fallbackAllowed, execOnce)
	if lastErr == nil {
		return resp, nil
	}
	if !fallbackAllowed {
		return cliproxyexecutor.Response{}, lastErr
	}
	if !m.shouldAllowRouteModelFallback(lastErr) {
		return cliproxyexecutor.Response{}, lastErr
	}
	for _, fallbackModel := range m.resolveFallbackModels(ctx, originalModel) {
		if _, duplicate := attempted[fallbackModel]; duplicate {
			continue
		}
		attempted[fallbackModel] = struct{}{}
		source := m.fallbackSourceForModel(originalModel, fallbackModel)
		resolvedActual := resolveActualModelName(fallbackModel)
		logEntryWithRequestID(ctx).WithFields(log.Fields{
			"requested_model":         strings.TrimSpace(originalModel),
			"fallback_model":          strings.TrimSpace(fallbackModel),
			"fallback_actual_model":   resolvedActual.actual,
			"fallback_model_is_alias": resolvedActual.isAlias,
			"fallback_source":         strings.TrimSpace(source),
		}).Infof("fallback chain activated: %s -> %s via %s", originalModel, fallbackModel, source)
		startedAt := time.Now()
		fallbackReq := req
		fallbackReq.Model = fallbackModel
		ctx = SetFallbackInfoInContext(ctx, originalModel, fallbackModel)
		fallbackProviders := m.ProvidersForRouteModel(fallbackModel)
		if len(fallbackProviders) == 0 {
			fallbackProviders = m.ProvidersForOAuthAliasWithoutRegisteredModels(fallbackModel)
		}
		if len(fallbackProviders) == 0 {
			fallbackProviders = providers
		}
		resp, errFallback := m.executeWithRetry(ctx, fallbackProviders, fallbackReq, opts, maxRetryCredentials, defaultRequestRetry, maxWait, true, execOnce)
		if errFallback == nil {
			logRouteModelFallbackResult(ctx, originalModel, fallbackModel, source, lastErr, nil, startedAt)
			return resp, nil
		}
		logRouteModelFallbackResult(ctx, originalModel, fallbackModel, source, lastErr, errFallback, startedAt)
		lastErr = errFallback
		if !m.shouldAllowRouteModelFallback(lastErr) {
			break
		}
	}
	return cliproxyexecutor.Response{}, lastErr
}

func (m *Manager) executeStreamWithRouteFallback(
	ctx context.Context,
	providers []string,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	execOnce func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options, int, *int, int, int) (*cliproxyexecutor.StreamResult, error),
) (*cliproxyexecutor.StreamResult, error) {
	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()
	originalModel := req.Model
	attempted := map[string]struct{}{originalModel: {}}
	fallbackAllowed := m.fallbackRetryAllowedForModel(originalModel)
	// See the equivalent comment in executeWithRouteFallback: the allowlist
	// state gates the retry loop itself (fallbackAllowed passed into
	// executeStreamWithRetry below), not maxRetryCredentials, since request-
	// level retry rounds run independently of the per-round credential limit.
	result, lastErr := m.executeStreamWithRetry(ctx, providers, req, opts, maxRetryCredentials, defaultRequestRetry, maxWait, fallbackAllowed, execOnce)
	if lastErr == nil {
		return result, nil
	}
	if !fallbackAllowed {
		return nil, lastErr
	}
	if !m.shouldAllowRouteModelFallback(lastErr) {
		return nil, lastErr
	}
	for _, fallbackModel := range m.resolveFallbackModels(ctx, originalModel) {
		if _, duplicate := attempted[fallbackModel]; duplicate {
			continue
		}
		attempted[fallbackModel] = struct{}{}
		source := m.fallbackSourceForModel(originalModel, fallbackModel)
		startedAt := time.Now()
		fallbackReq := req
		fallbackReq.Model = fallbackModel
		ctx = SetFallbackInfoInContext(ctx, originalModel, fallbackModel)
		fallbackProviders := m.ProvidersForRouteModel(fallbackModel)
		if len(fallbackProviders) == 0 {
			fallbackProviders = m.ProvidersForOAuthAliasWithoutRegisteredModels(fallbackModel)
		}
		if len(fallbackProviders) == 0 {
			fallbackProviders = providers
		}
		result, errFallback := m.executeStreamWithRetry(ctx, fallbackProviders, fallbackReq, opts, maxRetryCredentials, defaultRequestRetry, maxWait, true, execOnce)
		if errFallback == nil {
			logRouteModelFallbackResult(ctx, originalModel, fallbackModel, source, lastErr, nil, startedAt)
			return result, nil
		}
		logRouteModelFallbackResult(ctx, originalModel, fallbackModel, source, lastErr, errFallback, startedAt)
		lastErr = errFallback
		if !m.shouldAllowRouteModelFallback(lastErr) {
			break
		}
	}
	return nil, lastErr
}

func (m *Manager) shouldAllowRouteModelFallback(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch statusCodeFromError(err) {
	case http.StatusBadRequest:
		return true
	case http.StatusUnprocessableEntity:
		// Local: 422 activates route/model fallback and cools the failing auth
		// instead of stopping as a request-shape fault.
		return true
	case http.StatusNotFound:
		return isModelSupportErrorMessage(err.Error())
	default:
		return true
	}
}

func (m *Manager) executeWithRetry(
	ctx context.Context,
	providers []string,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	maxRetryCredentials int,
	defaultRequestRetry int,
	maxWait time.Duration,
	fallbackAllowed bool,
	execOnce func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options, int, int, int) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	var lastErr error
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	for attempt := 0; ; attempt++ {
		resp, errExec := execOnce(ctx, providers, req, opts, maxRetryCredentials, attempt, defaultRequestRetry)
		if errExec == nil {
			return resp, nil
		}
		if isRequestTerminatedError(errExec) || isRequestStopError(errExec) {
			return cliproxyexecutor.Response{}, unwrapRequestStopError(errExec)
		}
		lastErr = errExec
		if !fallbackAllowed {
			// The requested model is not on the fallback allowlist: the initial
			// attempt above already ran, but no post-error retry round may
			// follow it, regardless of request-retry or credential-retry
			// configuration.
			break
		}
		wait, shouldRetry := m.shouldRetryAfterErrorWithHomeRetryLimit(ctx, opts, errExec, attempt, providers, retryModel, maxWait, -1, defaultRequestRetry)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	return cliproxyexecutor.Response{}, lastErr
}

func (m *Manager) executeStreamWithRetry(
	ctx context.Context,
	providers []string,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	maxRetryCredentials int,
	defaultRequestRetry int,
	maxWait time.Duration,
	fallbackAllowed bool,
	execOnce func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options, int, *int, int, int) (*cliproxyexecutor.StreamResult, error),
) (*cliproxyexecutor.StreamResult, error) {
	var lastErr error
	homeRetryLimit := -1
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	attempt := 0
	retryRoundPending := false
	retryRoundWaited := false
	for {
		result, errStream := execOnce(ctx, providers, req, opts, maxRetryCredentials, &homeRetryLimit, attempt, defaultRequestRetry)
		if errStream == nil {
			return result, nil
		}
		if !fallbackAllowed {
			// The requested model is not on the fallback allowlist: the initial
			// attempt above already ran, but no post-error retry round may
			// follow it, regardless of home-retry, request-retry, or
			// credential-retry configuration.
			if isRequestTerminatedError(errStream) || isRequestStopError(errStream) {
				return nil, unwrapRequestStopError(errStream)
			}
			return nil, errStream
		}
		if m.HomeEnabled() && retryRoundPending {
			if wait, okWait := pendingHomeRetryRoundDelay(errStream, maxWait, &homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == ""); okWait && m.homeRetryAllowed(attempt-1, homeRetryLimit) {
				if retryRoundWaited {
					return nil, errStream
				}
				if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
					return nil, errWait
				}
				retryRoundWaited = true
				continue
			}
		}
		retryRoundPending = false
		retryRoundWaited = false
		if isRequestTerminatedError(errStream) || isRequestStopError(errStream) {
			return nil, unwrapRequestStopError(errStream)
		}
		lastErr = errStream
		wait, shouldRetry := m.shouldRetryAfterErrorWithHomeRetryLimit(ctx, opts, errStream, attempt, providers, retryModel, maxWait, homeRetryLimit, defaultRequestRetry)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return nil, errWait
		}
		attempt++
		retryRoundPending = m.HomeEnabled()
		retryRoundWaited = false
	}
	return nil, lastErr
}

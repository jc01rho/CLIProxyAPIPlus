package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
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

func (m *Manager) resolveFallbackModels(originalModel string) []string {
	candidates := make([]string, 0)
	seen := map[string]struct{}{originalModel: {}}
	if fallback, ok := m.getFallbackModel(originalModel); ok {
		if _, duplicate := seen[fallback]; !duplicate {
			candidates = append(candidates, fallback)
			seen[fallback] = struct{}{}
		}
	}
	for _, chainModel := range m.getFallbackChain() {
		if _, duplicate := seen[chainModel]; duplicate {
			continue
		}
		candidates = append(candidates, chainModel)
		seen[chainModel] = struct{}{}
	}
	if maxDepth := m.getFallbackMaxDepth(); len(candidates) > maxDepth {
		candidates = candidates[:maxDepth]
	}
	return candidates
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
		providerKey := executorKeyFromAuth(auth)
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
		providerKey := executorKeyFromAuth(auth)
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
	execOnce func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options, int) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	_, maxRetryCredentials, maxWait := m.retrySettings()
	originalModel := req.Model
	attempted := map[string]struct{}{originalModel: {}}
	resp, lastErr := m.executeWithRetry(ctx, providers, req, opts, maxRetryCredentials, maxWait, execOnce)
	if lastErr == nil {
		return resp, nil
	}
	if !m.shouldAllowRouteModelFallback(lastErr) {
		return cliproxyexecutor.Response{}, lastErr
	}
	for _, fallbackModel := range m.resolveFallbackModels(originalModel) {
		if _, duplicate := attempted[fallbackModel]; duplicate {
			continue
		}
		attempted[fallbackModel] = struct{}{}
		source := m.fallbackSourceForModel(originalModel, fallbackModel)
		startedAt := time.Now()
		fallbackReq := req
		fallbackReq.Model = fallbackModel
		fallbackProviders := m.ProvidersForRouteModel(fallbackModel)
		if len(fallbackProviders) == 0 {
			fallbackProviders = m.ProvidersForOAuthAliasWithoutRegisteredModels(fallbackModel)
		}
		if len(fallbackProviders) == 0 {
			fallbackProviders = providers
		}
		resp, errFallback := m.executeWithRetry(ctx, fallbackProviders, fallbackReq, opts, maxRetryCredentials, maxWait, execOnce)
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
	execOnce func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options, int) (*cliproxyexecutor.StreamResult, error),
) (*cliproxyexecutor.StreamResult, error) {
	_, maxRetryCredentials, maxWait := m.retrySettings()
	originalModel := req.Model
	attempted := map[string]struct{}{originalModel: {}}
	result, lastErr := m.executeStreamWithRetry(ctx, providers, req, opts, maxRetryCredentials, maxWait, execOnce)
	if lastErr == nil {
		return result, nil
	}
	if !m.shouldAllowRouteModelFallback(lastErr) {
		return nil, lastErr
	}
	for _, fallbackModel := range m.resolveFallbackModels(originalModel) {
		if _, duplicate := attempted[fallbackModel]; duplicate {
			continue
		}
		attempted[fallbackModel] = struct{}{}
		source := m.fallbackSourceForModel(originalModel, fallbackModel)
		startedAt := time.Now()
		fallbackReq := req
		fallbackReq.Model = fallbackModel
		fallbackProviders := m.ProvidersForRouteModel(fallbackModel)
		if len(fallbackProviders) == 0 {
			fallbackProviders = m.ProvidersForOAuthAliasWithoutRegisteredModels(fallbackModel)
		}
		if len(fallbackProviders) == 0 {
			fallbackProviders = providers
		}
		result, errFallback := m.executeStreamWithRetry(ctx, fallbackProviders, fallbackReq, opts, maxRetryCredentials, maxWait, execOnce)
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
		return isModelSupportError(err)
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
	maxWait time.Duration,
	execOnce func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options, int) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, errExec := execOnce(ctx, providers, req, opts, maxRetryCredentials)
		if errExec == nil {
			return resp, nil
		}
		if isRequestTerminatedError(errExec) {
			return cliproxyexecutor.Response{}, errExec
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterError(errExec, attempt, providers, req.Model, maxWait)
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
	maxWait time.Duration,
	execOnce func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options, int) (*cliproxyexecutor.StreamResult, error),
) (*cliproxyexecutor.StreamResult, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		result, errStream := execOnce(ctx, providers, req, opts, maxRetryCredentials)
		if errStream == nil {
			return result, nil
		}
		if isRequestTerminatedError(errStream) {
			return nil, errStream
		}
		lastErr = errStream
		wait, shouldRetry := m.shouldRetryAfterError(errStream, attempt, providers, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return nil, errWait
		}
	}
	return nil, lastErr
}

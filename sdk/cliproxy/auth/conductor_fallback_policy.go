package auth

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// SetFallbackAllowedModels stores a defensive copy of the configured fallback
// allowlist. A nil or empty list disables the policy and preserves all
// existing retry and fallback-chain behavior.
func (m *Manager) SetFallbackAllowedModels(models []string) {
	if m == nil {
		return
	}
	copied := make([]string, 0, len(models))
	for _, model := range models {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" {
			continue
		}
		copied = append(copied, trimmed)
	}
	m.fallbackAllowedModels.Store(copied)
}

// getFallbackAllowedModels returns the currently configured fallback
// allowlist. Callers must not mutate the returned slice.
func (m *Manager) getFallbackAllowedModels() []string {
	if m == nil {
		return nil
	}
	models, ok := m.fallbackAllowedModels.Load().([]string)
	if !ok {
		return nil
	}
	return models
}

// FallbackAllowedModels returns a defensive copy of the current fallback
// allowlist for logging and diagnostics. Callers may freely mutate the
// returned slice without affecting the manager's stored policy.
func (m *Manager) FallbackAllowedModels() []string {
	models := m.getFallbackAllowedModels()
	if models == nil {
		return nil
	}
	copied := make([]string, len(models))
	copy(copied, models)
	return copied
}

// fallbackRetryAllowedForModel reports whether post-error credential retry and
// route-model fallback-chain/model mapping may run for the originally
// requested model. An empty or nil allowlist preserves all existing behavior.
// A nonempty allowlist enables fallback only when the requested model or its
// registry-resolved actual model matches an allowlist entry directly or by
// the entry's registry-resolved actual model, compared case-insensitively
// after trimming.
func (m *Manager) fallbackRetryAllowedForModel(requestedModel string) bool {
	allowed := m.getFallbackAllowedModels()
	if len(allowed) == 0 {
		return true
	}
	return fallbackAllowlistMatches(allowed, requestedModel)
}

// fallbackAllowlistMatches reports whether requestedModel matches any entry in
// allowed, directly or through registry-resolved actual-model identities on
// either side. Comparisons are case-insensitive after trimming. Unlike
// resolveActualModelName, which only resolves registry-registered model IDs,
// this policy-specific matcher also resolves requested or configured names
// that are themselves alias strings, since operators may list either side of
// an alias/actual pair in the allowlist.
func fallbackAllowlistMatches(allowed []string, requestedModel string) bool {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" || len(allowed) == 0 {
		return false
	}
	requestedActual := resolvePolicyModelActual(requestedModel)
	for _, entry := range allowed {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.EqualFold(entry, requestedModel) {
			return true
		}
		if requestedActual != "" && strings.EqualFold(entry, requestedActual) {
			return true
		}
		entryActual := resolvePolicyModelActual(entry)
		if entryActual != "" && strings.EqualFold(entryActual, requestedModel) {
			return true
		}
		if entryActual != "" && requestedActual != "" && strings.EqualFold(entryActual, requestedActual) {
			return true
		}
	}
	return false
}

// resolvePolicyModelActual returns the registry-resolved actual (upstream)
// model name for modelName, checked exclusively for fallback-allowlist
// matching. It first tries a registry-ID lookup (the same resolution
// resolveActualModelName performs); if that fails, it scans registered model
// infos for an entry whose alias matches modelName, since the registry
// indexes registrations by model ID only and does not maintain a separate
// alias index. This alias-string scan is intentionally isolated to the
// fallback-allowlist matcher and must not affect resolveActualModelName's
// unrelated callers (API-key whitelist checks and fallback logging).
func resolvePolicyModelActual(modelName string) string {
	trimmed := strings.TrimSpace(modelName)
	if trimmed == "" {
		return ""
	}
	if resolved := resolveActualModelName(trimmed); resolved.actual != "" {
		return resolved.actual
	}
	info := lookupModelInfoByAliasForPolicy(trimmed)
	if info == nil {
		return ""
	}
	target := strings.TrimSpace(info.ExecutionTarget)
	if target == "" {
		target = strings.TrimSpace(info.ID)
	}
	if target == "" {
		target = strings.TrimSpace(info.Alias)
	}
	return target
}

// lookupModelInfoByAliasForPolicy scans registered model infos for an entry
// whose alias matches modelName. Used only by the fallback-allowlist matcher.
func lookupModelInfoByAliasForPolicy(modelName string) *registry.ModelInfo {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil
	}
	for _, info := range registry.GetGlobalRegistry().GetAvailableModelInfos() {
		if info == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(info.Alias), modelName) {
			return info
		}
	}
	return nil
}

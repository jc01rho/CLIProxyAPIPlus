package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func estimatedInputTokensFromMetadata(meta map[string]any) (int, bool) {
	if len(meta) == 0 {
		return 0, false
	}
	value, ok := meta[cliproxyexecutor.EstimatedInputTokensMetadataKey]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, typed > 0
	case int64:
		return int(typed), typed > 0
	case float64:
		return int(typed), typed > 0
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil && parsed > 0
	default:
		return 0, false
	}
}

func authBillingClass(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if normalized := normalizeRuntimeBillingClass(auth.Attributes["billing_class"]); normalized != "" {
		return normalized
	}
	if normalized := normalizeRuntimeBillingClass(auth.Attributes["billing-class"]); normalized != "" {
		return normalized
	}
	if raw, ok := auth.Metadata["billing_class"].(string); ok {
		if normalized := normalizeRuntimeBillingClass(raw); normalized != "" {
			return normalized
		}
	}
	if raw, ok := auth.Metadata["billing-class"].(string); ok {
		return normalizeRuntimeBillingClass(raw)
	}
	return ""
}

func thresholdRuleTargetBillingClass(rule internalconfig.TokenThresholdRule) string {
	return normalizeRuntimeBillingClass(string(rule.BillingClass))
}

func thresholdRuleReason(rule internalconfig.TokenThresholdRule, count int) string {
	parts := []string{"threshold_rule"}
	if pattern := strings.TrimSpace(rule.ModelPattern); pattern != "" {
		parts = append(parts, fmt.Sprintf("pattern=%s", pattern))
	}
	if count > 0 {
		parts = append(parts, fmt.Sprintf("estimated_tokens=%d", count))
	}
	if rule.MinTokens > 0 {
		parts = append(parts, fmt.Sprintf("min_tokens=%d", rule.MinTokens))
	}
	if rule.MaxTokens > 0 {
		parts = append(parts, fmt.Sprintf("max_tokens=%d", rule.MaxTokens))
	}
	if target := thresholdRuleTargetBillingClass(rule); target != "" {
		parts = append(parts, fmt.Sprintf("target=%s", target))
	}
	return strings.Join(parts, " ")
}

func authDecisionLabel(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if label := strings.TrimSpace(auth.Label); label != "" {
		return label
	}
	return strings.TrimSpace(auth.ID)
}

func matchTokenThresholdRule(rules []internalconfig.TokenThresholdRule, routeModel string, count int) (internalconfig.TokenThresholdRule, bool) {
	model := strings.TrimSpace(routeModel)
	for _, rule := range rules {
		if !rule.Enabled || rule.MinTokens > 0 && count < rule.MinTokens || rule.MaxTokens > 0 && count > rule.MaxTokens {
			continue
		}
		if rule.MinTokens <= 0 && rule.MaxTokens <= 0 {
			continue
		}
		pattern := strings.TrimSpace(rule.ModelPattern)
		if pattern == "" {
			return rule, true
		}
		if matched, err := filepath.Match(pattern, model); err == nil && matched {
			return rule, true
		}
	}
	return internalconfig.TokenThresholdRule{}, false
}

func (m *Manager) thresholdRuleForRequest(routeModel string, opts cliproxyexecutor.Options) (internalconfig.TokenThresholdRule, bool) {
	if m == nil {
		return internalconfig.TokenThresholdRule{}, false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || len(cfg.Routing.TokenThresholdRules) == 0 {
		return internalconfig.TokenThresholdRule{}, false
	}
	count, ok := estimatedInputTokensFromMetadata(opts.Metadata)
	if !ok || count <= 0 {
		return internalconfig.TokenThresholdRule{}, false
	}
	return matchTokenThresholdRule(cfg.Routing.TokenThresholdRules, routeModel, count)
}

func (m *Manager) authMatchesThresholdRule(auth *Auth, routeModel string, opts cliproxyexecutor.Options) bool {
	rule, ok := m.thresholdRuleForRequest(routeModel, opts)
	if !ok {
		return true
	}
	return auth != nil && strings.EqualFold(authBillingClass(auth), thresholdRuleTargetBillingClass(rule))
}

func (m *Manager) annotateThresholdDecisionSelected(ctx context.Context, routeModel string, opts cliproxyexecutor.Options, provider string, auth *Auth) context.Context {
	rule, ok := m.thresholdRuleForRequest(routeModel, opts)
	if !ok {
		return ctx
	}
	count, _ := estimatedInputTokensFromMetadata(opts.Metadata)
	reasonParts := []string{thresholdRuleReason(rule, count)}
	if provider = strings.TrimSpace(provider); provider != "" {
		reasonParts = append(reasonParts, fmt.Sprintf("provider=%s", provider))
	}
	if authName := authDecisionLabel(auth); authName != "" {
		reasonParts = append(reasonParts, fmt.Sprintf("auth=%s", authName))
	}
	selectedClass := authBillingClass(auth)
	if selectedClass != "" {
		reasonParts = append(reasonParts, fmt.Sprintf("selected_billing_class=%s", selectedClass))
	} else {
		selectedClass = thresholdRuleTargetBillingClass(rule)
	}
	return SetBillingDecisionInContext(ctx, selectedClass, strings.Join(reasonParts, " "))
}

func (m *Manager) annotateThresholdDecisionNoMatch(ctx context.Context, routeModel string, opts cliproxyexecutor.Options, suffix string) context.Context {
	rule, ok := m.thresholdRuleForRequest(routeModel, opts)
	if !ok {
		return ctx
	}
	count, _ := estimatedInputTokensFromMetadata(opts.Metadata)
	reason := thresholdRuleReason(rule, count)
	if suffix = strings.TrimSpace(suffix); suffix != "" {
		reason += " " + suffix
	}
	return SetBillingDecisionInContext(ctx, thresholdRuleTargetBillingClass(rule), reason)
}

func (m *Manager) filterProvidersForThreshold(routeModel string, providers []string, opts cliproxyexecutor.Options) []string {
	if m == nil || len(providers) == 0 {
		return providers
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || len(cfg.Routing.TokenThresholdRules) == 0 {
		return providers
	}
	count, ok := estimatedInputTokensFromMetadata(opts.Metadata)
	if !ok || count <= 0 {
		return providers
	}
	rule, ok := matchTokenThresholdRule(cfg.Routing.TokenThresholdRules, routeModel, count)
	if !ok {
		return providers
	}
	target := strings.TrimSpace(string(rule.BillingClass))
	if target == "" {
		return providers
	}
	matchedProviders := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, provider := range providers {
		providerKey := strings.ToLower(strings.TrimSpace(provider))
		if providerKey == "" {
			continue
		}
		if _, done := seen[providerKey]; done {
			continue
		}
		for _, auth := range m.auths {
			if auth == nil || auth.Disabled || strings.ToLower(strings.TrimSpace(auth.Provider)) != providerKey {
				continue
			}
			if strings.EqualFold(authBillingClass(auth), target) {
				matchedProviders = append(matchedProviders, providerKey)
				seen[providerKey] = struct{}{}
				break
			}
		}
	}
	if len(matchedProviders) == 0 {
		return providers
	}
	return matchedProviders
}

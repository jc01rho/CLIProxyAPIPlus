// Package common provides shared constants and utilities for Kiro translator.
package common

import (
	"strings"
)

// KiroUnsupportedContext1MSuffix is the Anthropic-only context-1m beta suffix.
// Kiro is AWS Bedrock-backed and does not honor it.
const KiroUnsupportedContext1MSuffix = "[1m]"

// HasUnsupportedKiroContextSuffix reports whether the model id contains [1m].
// [1m] is stripped by CanonicalKiroUpstreamID / mapModelToKiro rather than
// rejected upstream; this helper remains for telemetry / alias parsing.
func HasUnsupportedKiroContextSuffix(model string) bool {
	return strings.Contains(strings.ToLower(model), KiroUnsupportedContext1MSuffix)
}

const (
	KiroUnsupportedAgenticMessage  = "Kiro agentic aliases are not supported. The '-agentic' suffix did not change the upstream request; select a real Kiro model instead."
	KiroUnsupportedThinkingMessage = "This Kiro model does not support the '-thinking' alias. Use a model returned by Kiro's live catalog with Thinking capability."
)

// Hyphenated allowlists. Lookup always hyphenates first so
// "gpt-5.6-sol" and "gpt-5-6-sol" match the same entry.
// Adaptive envelope is the exact Kiro CLI 2.19.1 verified set.
var kiroAdaptiveThinkingModels = map[string]struct{}{
	"claude-opus-4-6":   {},
	"claude-opus-4-7":   {},
	"claude-opus-4-8":   {},
	"claude-opus-5":     {},
	"claude-sonnet-4-6": {},
}

var kiroNativeReasoningModels = map[string]struct{}{}

// CanonicalKiroUpstreamID strips kiro-/amazonq- prefixes and local suffixes,
// then hyphenates dots so allowlist lookups are stable.
func CanonicalKiroUpstreamID(modelID string) string {
	return strings.ReplaceAll(NormalizeKiroModelID(modelID), ".", "-")
}

// SupportsKiroAdaptiveThinking reports whether the model accepts Kiro's
// Claude additionalModelRequestFields adaptive envelope.
func SupportsKiroAdaptiveThinking(model string) bool {
	_, ok := kiroAdaptiveThinkingModels[CanonicalKiroUpstreamID(model)]
	return ok
}

// SupportsKiroNativeReasoning reports whether the model uses Kiro's native
// reasoning.effort field instead of the Claude envelope.
func SupportsKiroNativeReasoning(model string) bool {
	_, ok := kiroNativeReasoningModels[CanonicalKiroUpstreamID(model)]
	return ok
}

// IsObsoleteKiroRequestAlias reports request IDs that must not be forwarded
// to Kiro. Local static catalog still advertises -agentic / kiro-auto for
// offline fallback; those stay executable. kiro-lb maps auto-kiro → auto
// and strips Anthropic [1m] rather than rejecting either. -thinking on
// non-adaptive models is rejected.
func IsObsoleteKiroRequestAlias(modelID string) bool {
	_ = modelID
	return false
}

// RejectKiroRequestedModel returns a client-facing reason if the model must
// not be sent upstream. Empty string means the request may proceed.
// [1m] is stripped by CanonicalKiroUpstreamID / mapModelToKiro, not rejected.
func RejectKiroRequestedModel(modelID string) string {
	_ = modelID
	return ""
}

// KiroThinkingEffortLevels is the set of effort values Kiro accepts.
var KiroThinkingEffortLevels = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
	"max":    {},
}

// KiroThinkingBudgetForEffort returns a soft <max_thinking_length> hint.
func KiroThinkingBudgetForEffort(effort string) int {
	switch NormalizeKiroThinkingEffort(effort) {
	case "max":
		return 120000
	case "xhigh":
		return 64000
	case "high":
		return 32000
	case "medium":
		return 16000
	case "low":
		return 8000
	default:
		return 16000
	}
}

// NormalizeKiroThinkingEffort returns a canonical effort or "".
func NormalizeKiroThinkingEffort(effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "minimal" {
		effort = "low"
	}
	if _, ok := KiroThinkingEffortLevels[effort]; ok {
		return effort
	}
	return ""
}

// EffortFromThinkingBudget maps an Anthropic budget_tokens value to effort
// using kiro-lb's ratio against a default 8000-token ceiling.
func EffortFromThinkingBudget(budget int64) string {
	return EffortFromThinkingBudgetWithMax(budget, 8000)
}

// EffortFromThinkingBudgetWithMax maps budget_tokens using the request ceiling.
func EffortFromThinkingBudgetWithMax(budget, maxTokens int64) string {
	if budget < 1024 {
		return ""
	}
	ceiling := maxTokens
	if ceiling <= 0 {
		ceiling = 8000
	}
	ratio := float64(budget) / float64(ceiling)
	switch {
	case ratio >= 0.9:
		return "max"
	case ratio >= 0.7:
		return "xhigh"
	case ratio >= 0.4:
		return "high"
	case ratio >= 0.2:
		return "medium"
	default:
		return "low"
	}
}

// KiroThinkingPlan describes how a request should advertise thinking to Kiro.
type KiroThinkingPlan struct {
	InjectPrompt     bool
	ThinkingLength   int
	NativeReasoning  bool
	AdaptiveThinking bool
	Effort           string
	Fields           map[string]any
}

// PlanKiroThinking builds the kiro-lb-aligned thinking plan.
// additionalModelRequestFields only contain the verified adaptive envelope
// (thinking.type=adaptive + output_config.effort). Unknown members 400.
func PlanKiroThinking(modelID string, thinkingEnabled bool, effort string, maxTokens int64) KiroThinkingPlan {
	plan := KiroThinkingPlan{
		Effort:           NormalizeKiroThinkingEffort(effort),
		AdaptiveThinking: SupportsKiroAdaptiveThinking(modelID),
		NativeReasoning:  SupportsKiroNativeReasoning(modelID),
	}
	if thinkingEnabled && plan.Effort == "" {
		plan.Effort = "high"
	}
	if !thinkingEnabled && plan.Effort == "" {
		return plan
	}

	switch {
	case plan.AdaptiveThinking:
		plan.Fields = map[string]any{
			"output_config": map[string]any{"effort": plan.Effort},
			"thinking":      map[string]any{"type": "adaptive", "display": "summarized"},
		}
	default:
		// Unsupported models omit request-side thinking fields. The current
		// Kiro CLI does not emulate reasoning through prompt tags.
	}
	return plan
}

// ThinkingDirective returns the <thinking_mode> prompt prefix.
func ThinkingDirective(length int) string {
	if length <= 0 {
		length = 16000
	}
	return "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>" + itoa(length) + "</max_thinking_length>"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

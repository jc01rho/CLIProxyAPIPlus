package proto

import "strings"

// cursorModelAliases normalizes client-facing spelling variants to the exact
// wire ids cursor's server routes on.
var cursorModelAliases = map[string]string{
	"":                      "composer-2.5",
	"composer-2-5":          "composer-2.5",
	"composer-2.5-sdk":      "composer-2.5",
	"composer-latest":       "composer-2.5",
	"composer-2-5-fast":     "composer-2.5-fast",
	"composer-2.5-sdk-fast": "composer-2.5-fast",
	"composer-latest-fast":  "composer-2.5-fast",
	"grok":                  "grok-4.5",
	"grok-4":                "grok-4.5",
	"grok-fast":             "grok-4.5-fast",
	"cursor-grok-4.5":       "grok-4.5",
	"cursor-grok-4.6":       "grok-4.6",
	"claude-sonnet-4.6":     "claude-4.6-sonnet",
	"claude-opus-4.6":       "claude-4.6-opus",
	"claude-sonnet-4.5":     "claude-4.5-sonnet",
	"claude-opus-4.5":       "claude-4.5-opus",
	"claude-sonnet-4":       "claude-4-sonnet",
	"claude-opus-4":         "claude-4-opus",
}

var cursorRoutingLevels = []string{"cost", "balance", "intelligence"}

// Longest first so "-xhigh" is not consumed as "-high".
var cursorEffortSuffixesLongestFirst = []string{"xhigh", "medium", "high", "max", "low", "none"}

// cursorModelEffortTiers mirrors opencodex CURSOR_MODEL_EFFORT_TIERS.
// Bare ids in this map receive the last (highest) tier as a wire suffix.
var cursorModelEffortTiers = map[string][]string{
	"claude-4.5-opus":    {"high"},
	"claude-4.6-opus":    {"high", "max"},
	"claude-4.6-sonnet":  {"medium"},
	"claude-fable-5":     {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-4-7":    {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-4-8":    {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-5":      {"low", "medium", "high", "xhigh", "max"},
	"claude-sonnet-5":    {"low", "medium", "high", "xhigh", "max"},
	"glm-5.2":            {"high", "max"},
	"glm-5.3":            {"low", "high", "max"},
	"kimi-k3":            {"low", "high", "max"},
	"grok-4.5":           {"low", "medium", "high"},
	"grok-4.5-fast":      {"low", "medium", "high"},
	"grok-4.6":           {"low", "medium", "high", "xhigh"},
	"grok-4.6-fast":      {"low", "medium", "high", "xhigh"},
	"gpt-5.1":            {"low", "high"},
	"gpt-5.1-codex-max":  {"low", "medium", "high", "xhigh"},
	"gpt-5.1-codex-mini": {"low", "high"},
	"gpt-5.2":            {"low", "high", "xhigh"},
	"gpt-5.2-codex":      {"low", "high", "xhigh"},
	"gpt-5.3-codex":      {"low", "high", "xhigh"},
	"gpt-5.4":            {"low", "medium", "high", "xhigh"},
	"gpt-5.4-mini":       {"low", "medium", "high", "xhigh"},
	"gpt-5.4-nano":       {"low", "medium", "high", "xhigh"},
	"gpt-5.5":            {"low", "medium", "high"},
	"gpt-5.5-extra":      {"high"},
	"gpt-5.6-sol":        {"low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-terra":      {"low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-luna":       {"low", "medium", "high", "xhigh", "max"},
}

// ResolveRequestedModel maps a client model id (plus optional OpenAI
// reasoning_effort) onto the wire id / ModelParameter pair that opencodex
// and cursor-agent send to AgentService/Run.
//
//	"auto" / "auto-{cost,balance,intelligence}" stay parameterized defaults
//	"composer-*-fast" stays {fast:true}
//	known effort models keep or append the suffix on requested_model
//	grok-4.5 / grok-4.6 get a cursor- prefix; *-fast is parameterized
func ResolveRequestedModel(modelID, reasoningEffort string) (string, []ModelParameter) {
	normalized := normalizeCursorModelID(modelID)
	if normalized == "auto" {
		return "default", nil
	}
	for _, level := range cursorRoutingLevels {
		if normalized == "auto-"+level {
			return "default", []ModelParameter{{ID: "optimization", Value: level}}
		}
	}
	// Probe the live-catalog legacy-variant alias table ahead of the static
	// tier tables (senpi's suffixAliasId, cursor/selection-descriptor.ts:29-44),
	// trying both thinking-infix spellings. A hit always carries zero
	// ModelParameters, matching senpi's legacy-variant encoding.
	for _, candidate := range cursorThinkingInfixCandidates(strings.ToLower(normalized)) {
		if wireID, ok := resolveCursorVariantAlias(candidate); ok {
			return wireID, nil
		}
	}
	base, existing, fast := parseCursorRequestedModel(normalized)
	effort := resolveCursorEffort(base, existing, reasoningEffort, fast)
	return composeCursorWireModel(base, effort, fast)
}

func normalizeCursorModelID(modelID string) string {
	id := strings.TrimSpace(modelID)
	id = strings.TrimPrefix(id, "cursor/")
	if alias, ok := cursorModelAliases[strings.ToLower(id)]; ok {
		return alias
	}
	return id
}

// resolveCursorVariantAlias probes the live-catalog legacy-variant alias table
// (cursorVariantAliases, ported from senpi's cursor-variant-aliases.json) for an
// exact hit on the normalized id. senpi's suffixAliasId (cursor/selection-descriptor.ts)
// tries the thinking-infixed id in both orders (`<cap>-thinking-<sfx>` then
// `<cap>-<sfx>-thinking`) before falling back to the non-thinking suffix form, and on
// a hit returns the alias's legacyVariantId (== the alias table key itself) as the
// wire id -- NOT targetId, which is the bare capability id without the level suffix.
// Every entry resolves with zero ModelParameters, matching senpi's legacy-variant
// encoding.
//
// Bare ids (no level suffix) that are also present in the static effort-tier
// table are left to the static path: senpi's catalog lists those bases as
// self-referential passthrough aliases (targetId == key, no level), which carry
// no tier information, while the static table's bare-id behavior is to promote
// to the highest tier (mirroring senpi's own "no explicit selection" default of
// the representative variant). Only ids the static table has no opinion on, or
// aliases that actually carry a level/suffix, are resolved here.
func resolveCursorVariantAlias(id string) (string, bool) {
	alias, ok := cursorVariantAliases[strings.ToLower(id)]
	if !ok {
		return "", false
	}
	if alias.level == "" && cursorModelHasEffortTiers(id) {
		return "", false
	}
	return id, true
}

// cursorThinkingInfixCandidates returns the dual thinking-infix spellings senpi's
// suffixAliasId tries for a thinking-capable base id: `<cap>-thinking-<sfx>` and
// `<cap>-<sfx>-thinking`. Only returns a second candidate when id matches one of
// the two forms, so a plain (non-thinking) id passes through unchanged.
func cursorThinkingInfixCandidates(id string) []string {
	const thinkingMarker = "-thinking"
	// <head>-<sfx>-thinking -> also try <head>-thinking-<sfx>
	for _, suffix := range cursorEffortSuffixesLongestFirst {
		marker := "-" + suffix + thinkingMarker
		if strings.HasSuffix(id, marker) {
			head := strings.TrimSuffix(id, marker)
			return []string{id, head + thinkingMarker + "-" + suffix}
		}
	}
	// <head>-thinking-<sfx> -> also try <head>-<sfx>-thinking
	if idx := strings.Index(id, thinkingMarker+"-"); idx >= 0 {
		head := id[:idx]
		suffix := id[idx+len(thinkingMarker)+1:]
		if suffix != "" {
			return []string{id, head + "-" + suffix + thinkingMarker}
		}
	}
	return []string{id}
}

// splitCursorFastSuffix separates a trailing "-fast" marker from a model id.
// Cursor spells the fast identities with `-fast` last (`cursor-grok-4.5-low-fast`),
// but clients also send it mid-id (`grok-4.5-fast-low`), so the marker is stripped
// wherever it lands rather than only at the end of the raw input.
func splitCursorFastSuffix(id string) (string, bool) {
	if trimmed := strings.TrimSuffix(id, "-fast"); trimmed != id {
		return trimmed, true
	}
	return id, false
}

func parseCursorRequestedModel(id string) (base, effort string, fast bool) {
	id = strings.TrimPrefix(id, "cursor-")
	id, fast = splitCursorFastSuffix(id)
	for _, suffix := range cursorEffortSuffixesLongestFirst {
		marker := "-" + suffix
		if !strings.HasSuffix(id, marker) {
			continue
		}
		candidate, candidateFast := splitCursorFastSuffix(strings.TrimSuffix(id, marker))
		fast = fast || candidateFast
		if aliased, ok := cursorModelAliases[strings.ToLower(candidate)]; ok {
			candidate, _ = splitCursorFastSuffix(aliased)
		}
		if cursorModelHasEffortTiers(candidate+"-fast") || cursorModelHasEffortTiers(candidate) {
			return candidate, suffix, fast
		}
	}
	if aliased, ok := cursorModelAliases[strings.ToLower(id)]; ok {
		var aliasFast bool
		id, aliasFast = splitCursorFastSuffix(aliased)
		fast = fast || aliasFast
	}
	return id, "", fast
}

func cursorModelHasEffortTiers(id string) bool {
	_, ok := cursorModelEffortTiers[id]
	return ok
}

func resolveCursorEffort(base, existing, reasoning string, fast bool) string {
	lookup := base
	if fast {
		if cursorModelHasEffortTiers(base + "-fast") {
			lookup = base + "-fast"
		}
	}
	tiers, ok := cursorModelEffortTiers[lookup]
	if !ok {
		return ""
	}
	if existing != "" && containsString(tiers, existing) {
		return existing
	}
	return mapReasoningToCursorTier(reasoning, tiers)
}

func mapReasoningToCursorTier(reasoning string, tiers []string) string {
	if len(tiers) == 0 {
		return ""
	}
	r := strings.ToLower(strings.TrimSpace(reasoning))
	if r != "" && containsString(tiers, r) {
		return r
	}
	switch r {
	case "none", "low", "minimal":
		return tiers[0]
	case "high", "xhigh", "extra_high", "extra-high", "max", "highest":
		return tiers[len(tiers)-1]
	case "medium", "mid", "default":
		return tiers[(len(tiers)-1)/2]
	default:
		return tiers[len(tiers)-1]
	}
}

// cursorGrokCapabilityBases are the grok families whose cursor catalog identity
// carries the `cursor-` capability prefix (`cursor-grok-4.5`, `cursor-grok-4.6`).
var cursorGrokCapabilityBases = map[string]bool{"grok-4.5": true, "grok-4.6": true}

// composeCursorWireModel renders the resolved base + effort + fast triple as the
// suffix-variant id cursor's catalog serves. senpi resolves the same triple
// through resolveCursorSelectionDescriptor, which returns a served suffix-variant
// id with EMPTY parameters; its buildParameters never emits `fast:"true"`. The
// grok fast identities are served as `cursor-grok-<ver>-<effort>-fast`, so the
// `-fast` marker trails the effort suffix.
func composeCursorWireModel(base, effort string, fast bool) (string, []ModelParameter) {
	if cursorGrokCapabilityBases[base] {
		wire := "cursor-" + base
		if effort != "" {
			wire += "-" + effort
		}
		if fast {
			wire += "-fast"
		}
		return wire, nil
	}
	if fast && strings.HasPrefix(base, "composer-") {
		return base + "-fast", nil
	}
	if effort != "" {
		return base + "-" + effort, nil
	}
	return base, nil
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

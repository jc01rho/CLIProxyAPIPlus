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
	if strings.HasPrefix(normalized, "composer-") && strings.HasSuffix(normalized, "-fast") {
		return strings.TrimSuffix(normalized, "-fast"),
			[]ModelParameter{{ID: "fast", Value: "true"}}
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

func parseCursorRequestedModel(id string) (base, effort string, fast bool) {
	id = strings.TrimPrefix(id, "cursor-")
	if strings.HasSuffix(id, "-fast") {
		fast = true
		id = strings.TrimSuffix(id, "-fast")
	}
	for _, suffix := range cursorEffortSuffixesLongestFirst {
		marker := "-" + suffix
		if !strings.HasSuffix(id, marker) {
			continue
		}
		candidate := strings.TrimSuffix(id, marker)
		if aliased, ok := cursorModelAliases[strings.ToLower(candidate)]; ok {
			candidate = aliased
		}
		lookup := candidate
		if fast {
			lookup = candidate + "-fast"
		}
		if cursorModelHasEffortTiers(lookup) || cursorModelHasEffortTiers(candidate) {
			return candidate, suffix, fast
		}
	}
	if aliased, ok := cursorModelAliases[strings.ToLower(id)]; ok {
		id = aliased
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

func composeCursorWireModel(base, effort string, fast bool) (string, []ModelParameter) {
	if fast && (base == "grok-4.5" || base == "grok-4.6") {
		params := make([]ModelParameter, 0, 2)
		if effort != "" {
			params = append(params, ModelParameter{ID: "effort", Value: effort})
		}
		params = append(params, ModelParameter{ID: "fast", Value: "true"})
		return base, params
	}
	if fast && strings.HasPrefix(base, "composer-") {
		return base, []ModelParameter{{ID: "fast", Value: "true"}}
	}
	if effort != "" {
		wire := base + "-" + effort
		if base == "grok-4.5" || base == "grok-4.6" {
			wire = "cursor-" + wire
		}
		return wire, nil
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

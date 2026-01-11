package auth

import (
	"strings"
	"sync"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

type modelNameMappingTable struct {
	// reverse maps channel -> alias (lower) -> list of original upstream model names.
	// Multiple upstream models can map to the same alias (e.g., opus -> [kiro-claude-opus-4-5-agentic, kiro-claude-opus-4-5]).
	// Round-robin is applied across these models during resolution.
	reverse map[string]map[string][]string

	// cursors tracks round-robin state per channel:alias for multi-model aliases.
	mu      sync.Mutex
	cursors map[string]int
}

func compileModelNameMappingTable(mappings map[string][]internalconfig.ModelNameMapping) *modelNameMappingTable {
	if len(mappings) == 0 {
		return &modelNameMappingTable{}
	}
	out := &modelNameMappingTable{
		reverse: make(map[string]map[string][]string, len(mappings)),
	}
	for rawChannel, entries := range mappings {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(entries) == 0 {
			continue
		}
		rev := make(map[string][]string, len(entries))
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			aliasKey := strings.ToLower(alias)
			// Allow multiple upstream names to map to the same alias.
			// They are tried in order during resolution.
			rev[aliasKey] = append(rev[aliasKey], name)
		}
		if len(rev) > 0 {
			out.reverse[channel] = rev
		}
	}
	if len(out.reverse) == 0 {
		out.reverse = nil
	}
	return out
}

// SetOAuthModelMappings updates the OAuth model name mapping table used during execution.
// The mapping is applied per-auth channel to resolve the upstream model name while keeping the
// client-visible model name unchanged for translation/response formatting.
func (m *Manager) SetOAuthModelMappings(mappings map[string][]internalconfig.ModelNameMapping) {
	if m == nil {
		return
	}
	table := compileModelNameMappingTable(mappings)
	// atomic.Value requires non-nil store values.
	if table == nil {
		table = &modelNameMappingTable{}
	}
	m.modelNameMappings.Store(table)
}

// applyOAuthModelMapping resolves the upstream model from OAuth model mappings
// and returns the resolved model along with updated metadata. If a mapping exists,
// the returned model is the upstream model and metadata contains the original
// requested model for response translation.
func (m *Manager) applyOAuthModelMapping(auth *Auth, requestedModel string, metadata map[string]any) (string, map[string]any) {
	upstreamModel := m.resolveOAuthUpstreamModel(auth, requestedModel)
	if upstreamModel == "" {
		return requestedModel, metadata
	}
	out := make(map[string]any, 1)
	if len(metadata) > 0 {
		out = make(map[string]any, len(metadata)+1)
		for k, v := range metadata {
			out[k] = v
		}
	}
	// Store the requested alias (e.g., "gp") so downstream can use it to look up
	// model metadata from the global registry where it was registered under this alias.
	out[util.ModelMappingOriginalModelMetadataKey] = requestedModel
	return upstreamModel, out
}

func (m *Manager) resolveOAuthUpstreamModel(auth *Auth, requestedModel string) string {
	if m == nil || auth == nil {
		return ""
	}
	channel := modelMappingChannel(auth)
	if channel == "" {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(requestedModel))
	if key == "" {
		return ""
	}
	raw := m.modelNameMappings.Load()
	table, _ := raw.(*modelNameMappingTable)
	if table == nil || table.reverse == nil {
		return ""
	}
	rev := table.reverse[channel]
	if rev == nil {
		return ""
	}
	// Multiple upstream names may map to the same alias.
	names := rev[key]
	if len(names) == 0 {
		return ""
	}

	// Single model: return directly without cursor overhead (existing behavior).
	if len(names) == 1 {
		original := strings.TrimSpace(names[0])
		if original == "" || strings.EqualFold(original, requestedModel) {
			return ""
		}
		return original
	}

	// Multiple models: apply round-robin selection with cursor increment.
	cursorKey := channel + ":" + key
	table.mu.Lock()
	if table.cursors == nil {
		table.cursors = make(map[string]int)
	}
	index := table.cursors[cursorKey]
	table.cursors[cursorKey] = (index + 1) % len(names)
	table.mu.Unlock()

	original := strings.TrimSpace(names[index%len(names)])
	if original == "" || strings.EqualFold(original, requestedModel) {
		return ""
	}
	return original
}

// GetRemainingUpstreamModels returns the remaining upstream models for fallback after
// the first model (returned by resolveOAuthUpstreamModel) fails. This function does NOT
// modify the cursor - it's read-only for fallback purposes.
// Returns nil if no mapping exists, single model mapping, or no remaining models.
func (m *Manager) GetRemainingUpstreamModels(auth *Auth, requestedModel string) []string {
	if m == nil || auth == nil {
		return nil
	}
	channel := modelMappingChannel(auth)
	if channel == "" {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(requestedModel))
	if key == "" {
		return nil
	}
	raw := m.modelNameMappings.Load()
	table, _ := raw.(*modelNameMappingTable)
	if table == nil || table.reverse == nil {
		return nil
	}
	rev := table.reverse[channel]
	if rev == nil {
		return nil
	}
	names := rev[key]
	// No remaining models for single-model mappings or empty mappings.
	if len(names) <= 1 {
		return nil
	}

	// Read current cursor position (no modification - read-only for fallback).
	// resolveOAuthUpstreamModel already incremented the cursor, so currentIndex
	// points to the NEXT model. The last used model is at (currentIndex - 1).
	cursorKey := channel + ":" + key
	table.mu.Lock()
	currentIndex := 0
	if table.cursors != nil {
		currentIndex = table.cursors[cursorKey]
	}
	table.mu.Unlock()

	// Calculate the index of the model that was just returned by resolveOAuthUpstreamModel.
	// Since cursor was incremented after selection, lastUsedIndex = currentIndex - 1.
	lastUsedIndex := (currentIndex - 1 + len(names)) % len(names)

	// Build remaining models list, starting from the model after lastUsedIndex.
	remaining := make([]string, 0, len(names)-1)
	for i := 1; i < len(names); i++ {
		idx := (lastUsedIndex + i) % len(names)
		model := strings.TrimSpace(names[idx])
		if model != "" {
			remaining = append(remaining, model)
		}
	}
	if len(remaining) == 0 {
		return nil
	}
	return remaining
}

// modelMappingChannel extracts the OAuth model mapping channel from an Auth object.
// It determines the provider and auth kind from the Auth's attributes and delegates
// to OAuthModelMappingChannel for the actual channel resolution.
func modelMappingChannel(auth *Auth) string {
	if auth == nil {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	authKind := ""
	if auth.Attributes != nil {
		authKind = strings.ToLower(strings.TrimSpace(auth.Attributes["auth_kind"]))
	}
	if authKind == "" {
		if kind, _ := auth.AccountInfo(); strings.EqualFold(kind, "api_key") {
			authKind = "apikey"
		}
	}
	return OAuthModelMappingChannel(provider, authKind)
}

// OAuthModelMappingChannel returns the OAuth model mapping channel name for a given provider
// and auth kind. Returns empty string if the provider/authKind combination doesn't support
// OAuth model mappings (e.g., API key authentication).
//
// Supported channels: gemini-cli, vertex, aistudio, antigravity, claude, codex, qwen, iflow, kiro, github-copilot.
func OAuthModelMappingChannel(provider, authKind string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authKind = strings.ToLower(strings.TrimSpace(authKind))
	switch provider {
	case "gemini":
		// gemini provider uses gemini-api-key config, not oauth-model-mappings.
		// OAuth-based gemini auth is converted to "gemini-cli" by the synthesizer.
		return ""
	case "vertex":
		if authKind == "apikey" {
			return ""
		}
		return "vertex"
	case "claude":
		if authKind == "apikey" {
			return ""
		}
		return "claude"
	case "codex":
		if authKind == "apikey" {
			return ""
		}
		return "codex"
	case "gemini-cli", "aistudio", "antigravity", "qwen", "iflow":
		return provider
	case "kiro", "github-copilot":
		return provider
	default:
		return ""
	}
}

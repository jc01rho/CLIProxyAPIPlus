package config

import "strings"

// defaultKiroAliases returns default oauth-model-alias entries for Kiro.
// These aliases expose standard Claude IDs for Kiro-prefixed upstream models.
func defaultKiroAliases() []OAuthModelAlias {
	return []OAuthModelAlias{
		// Sonnet 4.6
		{Name: "kiro-claude-sonnet-4-6", Alias: "claude-sonnet-4-6", Fork: true},
		// Sonnet 4.5
		{Name: "kiro-claude-sonnet-4-5", Alias: "claude-sonnet-4-5-20250929", Fork: true},
		{Name: "kiro-claude-sonnet-4-5", Alias: "claude-sonnet-4-5", Fork: true},
		// Sonnet 4
		{Name: "kiro-claude-sonnet-4", Alias: "claude-sonnet-4-20250514", Fork: true},
		{Name: "kiro-claude-sonnet-4", Alias: "claude-sonnet-4", Fork: true},
		// Opus 4.6
		{Name: "kiro-claude-opus-4-6", Alias: "claude-opus-4-6", Fork: true},
		// Opus 4.5
		{Name: "kiro-claude-opus-4-5", Alias: "claude-opus-4-5-20251101", Fork: true},
		{Name: "kiro-claude-opus-4-5", Alias: "claude-opus-4-5", Fork: true},
		// Haiku 4.5
		{Name: "kiro-claude-haiku-4-5", Alias: "claude-haiku-4-5-20251001", Fork: true},
		{Name: "kiro-claude-haiku-4-5", Alias: "claude-haiku-4-5", Fork: true},
	}
}

// defaultGitHubCopilotAliases returns default oauth-model-alias entries for
// GitHub Copilot Claude models. It exposes hyphen-style IDs used by clients.
func defaultGitHubCopilotAliases() []OAuthModelAlias {
	return []OAuthModelAlias{
		{Name: "claude-haiku-4.5", Alias: "claude-haiku-4-5", Fork: true},
		{Name: "claude-opus-4.1", Alias: "claude-opus-4-1", Fork: true},
		{Name: "claude-opus-4.5", Alias: "claude-opus-4-5", Fork: true},
		{Name: "claude-opus-4.6", Alias: "claude-opus-4-6", Fork: true},
		{Name: "claude-sonnet-4.5", Alias: "claude-sonnet-4-5", Fork: true},
		{Name: "claude-sonnet-4.6", Alias: "claude-sonnet-4-6", Fork: true},
	}
}

// defaultQoderAliases returns default oauth-model-alias entries for Qoder.
// Qoder exposes short model IDs (lite, auto, …) over an OpenAI-compatible
// API.  The aliases allow clients to use the short form while the registry
// stores the prefixed "qoder-*" variant.
func defaultQoderAliases() []OAuthModelAlias {
	return []OAuthModelAlias{
		{Name: "qoder-lite", Alias: "lite", Fork: true},
		{Name: "qoder-auto", Alias: "auto", Fork: true},
		{Name: "qoder-efficient", Alias: "efficient", Fork: true},
		{Name: "qoder-performance", Alias: "performance", Fork: true},
		{Name: "qoder-ultimate", Alias: "ultimate", Fork: true},
		{Name: "qoder-q35model_preview", Alias: "q35model_preview", Fork: true},
		{Name: "qoder-qmodel", Alias: "qmodel", Fork: true},
		{Name: "qoder-q35model", Alias: "q35model", Fork: true},
		{Name: "qoder-gmodel", Alias: "gmodel", Fork: true},
		{Name: "qoder-kmodel", Alias: "kmodel", Fork: true},
		{Name: "qoder-mmodel", Alias: "mmodel", Fork: true},
	}
}

// GitHubCopilotAliasesFromModels generates oauth-model-alias entries from a dynamic
// list of model IDs fetched from the Copilot API. It auto-creates aliases for
// models whose ID contains a dot (e.g. "claude-opus-4.6" → "claude-opus-4-6"),
// which is the pattern used by Claude models on Copilot.
func GitHubCopilotAliasesFromModels(modelIDs []string) []OAuthModelAlias {
	var aliases []OAuthModelAlias
	seen := make(map[string]struct{})
	for _, id := range modelIDs {
		if !strings.Contains(id, ".") {
			continue
		}
		hyphenID := strings.ReplaceAll(id, ".", "-")
		key := id + "→" + hyphenID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		aliases = append(aliases, OAuthModelAlias{Name: id, Alias: hyphenID, Fork: true})
	}
	return aliases
}

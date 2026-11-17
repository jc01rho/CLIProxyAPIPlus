package auth

import "strings"

// antigravityModelAliasChannel is the OAuth model alias channel that antigravity
// credentials resolve their route models through.
const antigravityModelAliasChannel = "antigravity"

// antigravityClaudeOrGPTModelName reports whether a model identifier names a
// Claude or GPT family model.
func antigravityClaudeOrGPTModelName(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "claude") || strings.Contains(lower, "gpt")
}

// antigravityClaudeOrGPTRoute reports whether the request targets a Claude or GPT
// family model, either literally or through the OAuth model alias the identifier
// resolves to.
//
// Antigravity account fallback must never move a Claude or GPT request onto a
// different Google account, so the primary handoff and the credits retry both
// consult this guard before switching credentials. Alias resolution reads only
// snapshot state (auth attribute aliases plus the compiled alias table), so
// callers may hold the conductor lock.
func (m *Manager) antigravityClaudeOrGPTRoute(auth *Auth, models ...string) bool {
	for _, model := range models {
		if antigravityClaudeOrGPTModelName(model) {
			return true
		}
	}
	aliases := OAuthModelAliasesFromAttributes(authAttributes(auth))
	for _, model := range models {
		requested := strings.TrimSpace(model)
		if requested == "" {
			continue
		}
		if antigravityClaudeOrGPTModelName(resolveUpstreamModelFromAliases(aliases, requested).UpstreamModel) {
			return true
		}
		if antigravityClaudeOrGPTModelName(m.antigravityAliasTableUpstreamModel(requested)) {
			return true
		}
	}
	return false
}

// antigravityAliasTableUpstreamModel returns the upstream model that the compiled
// config alias table maps requestedModel to on the antigravity channel, or an
// empty string when the table holds no mapping.
func (m *Manager) antigravityAliasTableUpstreamModel(requestedModel string) string {
	if m == nil {
		return ""
	}
	table, _ := m.oauthModelAlias.Load().(*oauthModelAliasTable)
	if table == nil || table.reverse == nil {
		return ""
	}
	resolved := table.resolveTableOnly(antigravityModelAliasChannel, requestedModel)
	return strings.TrimSpace(resolved.tailResult.UpstreamModel)
}

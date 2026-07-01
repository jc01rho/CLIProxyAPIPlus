package cliproxy

import (
	"context"
	"strings"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// fetchClaudeModelsForAuth fetches available models from the Anthropic /v1/models
// endpoint using the OAuth access token on the auth record. Returns nil if the
// auth is not OAuth (no access_token) or the fetch fails — callers fall back to
// the static registry list.
func (s *Service) fetchClaudeModelsForAuth(ctx context.Context, auth *coreauth.Auth) []claudeauth.AnthropicModelEntry {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil
	}
	// Only OAuth tokens (sk-ant-oat*) can use Bearer auth against /v1/models.
	if !strings.Contains(accessToken, "sk-ant-oat") {
		return nil
	}

	proxyURL := ""
	if auth.ProxyURL != "" {
		proxyURL = auth.ProxyURL
	} else if s != nil && s.cfg != nil {
		proxyURL = strings.TrimSpace(s.cfg.ProxyURL)
	}

	ca := claudeauth.NewClaudeAuthWithProxyURL(s.cfg, proxyURL)
	resp, err := ca.ListModels(ctx, accessToken)
	if err != nil {
		log.Debugf("claude model fetch for auth %s: %v", auth.ID, err)
		return nil
	}
	return resp.Data
}

// mergeClaudeFetchedModels upserts dynamically-fetched model entries into the
// static registry list. Models from the API that are not in the static list are
// appended; existing entries are left untouched (static metadata is richer).
func mergeClaudeFetchedModels(static []*registry.ModelInfo, fetched []claudeauth.AnthropicModelEntry) []*registry.ModelInfo {
	if len(fetched) == 0 {
		return static
	}
	existing := make(map[string]struct{}, len(static))
	for _, m := range static {
		if m != nil {
			existing[strings.ToLower(m.ID)] = struct{}{}
		}
	}
	for _, entry := range fetched {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, ok := existing[strings.ToLower(id)]; ok {
			continue
		}
		static = append(static, entry.ToModelInfo())
		existing[strings.ToLower(id)] = struct{}{}
	}
	return static
}

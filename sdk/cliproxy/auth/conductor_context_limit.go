package auth

import (
	"context"
	"fmt"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// routingContextLimitEntry is implemented by configured model entries that cap
// the estimated input tokens routed to them (routing-max-context-length).
type routingContextLimitEntry interface {
	GetRoutingMaxContextLength() int
}

func routingContextLimitOf(entry modelAliasEntry) int {
	limited, ok := entry.(routingContextLimitEntry)
	if !ok {
		return 0
	}
	if limit := limited.GetRoutingMaxContextLength(); limit > 0 {
		return limit
	}
	return 0
}

// routingContextLimitForModels returns the routing context limit that applies
// when requestedModel resolves through models, or 0 when it is unlimited or
// nothing matches. It follows the precedence of
// resolveModelAliasPoolFromConfigModels: alias matches first, a direct name
// match only when no alias matched. When an alias pools several upstream
// models, the auth can still serve the request through the largest one, so the
// largest limit wins and any pooled entry without a limit makes it unlimited.
func routingContextLimitForModels(requestedModel string, models []modelAliasEntry) int {
	if len(models) == 0 {
		return 0
	}
	_, candidates := modelAliasLookupCandidates(requestedModel)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		matched := false
		limit := 0
		for _, entry := range models {
			alias := strings.TrimSpace(entry.GetAlias())
			if alias == "" || !strings.EqualFold(alias, candidate) {
				continue
			}
			entryLimit := routingContextLimitOf(entry)
			if entryLimit == 0 {
				return 0
			}
			matched = true
			limit = max(limit, entryLimit)
		}
		if matched {
			return limit
		}
	}
	for _, entry := range models {
		name := strings.TrimSpace(entry.GetName())
		if name == "" {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(name, candidate) {
				return routingContextLimitOf(entry)
			}
		}
	}
	return 0
}

// routingContextLimitForAuth returns the routing-max-context-length configured
// on the OpenAI-compatible model entry auth serves routeModel through; 0 means
// unlimited. Callers must hold at least m.mu.RLock while auth comes from m.auths.
func (m *Manager) routingContextLimitForAuth(auth *Auth, routeModel string) int {
	if m == nil || auth == nil || !isConfiguredOpenAICompatAuth(auth) {
		return 0
	}
	requestedModel := strings.TrimSpace(rewriteModelForAuth(routeModel, auth))
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(routeModel)
	}
	if requestedModel == "" {
		return 0
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	entry := resolveOpenAICompatConfigForAuth(m.loadAPIKeyModelRouting().config, auth, providerKey, compatName)
	if entry == nil {
		return 0
	}
	return routingContextLimitForModels(requestedModel, asModelAliasEntries(entry.Models))
}

// authExceedsRoutingContextLimit reports whether the request's estimated input
// tokens exceed the routing context limit of auth's model entry. Requests
// without an estimate are never excluded.
func (m *Manager) authExceedsRoutingContextLimit(auth *Auth, routeModel string, opts cliproxyexecutor.Options) (tokens int, limit int, exceeded bool) {
	tokens, ok := estimatedInputTokensFromMetadata(opts.Metadata)
	if !ok {
		return 0, 0, false
	}
	limit = m.routingContextLimitForAuth(auth, routeModel)
	return tokens, limit, limit > 0 && tokens > limit
}

// logRoutingContextLimitExcluded records a candidate skipped because the
// request is larger than its routing-max-context-length.
func (m *Manager) logRoutingContextLimitExcluded(ctx context.Context, provider, routeModel string, auth *Auth, tokens, limit int) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	const maxProviderFieldLen = 256
	if len(provider) > maxProviderFieldLen {
		provider = provider[:maxProviderFieldLen] + "..."
	}
	entry := log.WithField("provider", provider).
		WithField("auth_id", authID).
		WithField("model", routeModel).
		WithField("estimated_input_tokens", tokens).
		WithField("routing_max_context_length", limit)
	if reqID, ok := ctx.Value("request_id").(string); ok && reqID != "" {
		entry = entry.WithField("request_id", reqID)
	}
	entry.Debug("auth excluded by routing context limit")
}

// authNotFoundAfterContextLimit reports a selection that found no candidate
// once the routing context limit was applied. It keeps the auth_not_found code
// so model fallback chains still run, and names the estimate so the cause is
// visible to the caller.
func authNotFoundAfterContextLimit(tokens int, timeGateExcluded bool) error {
	err := &Error{
		Code:    "auth_not_found",
		Message: fmt.Sprintf("no auth available: estimated %d input tokens exceed routing-max-context-length of every remaining candidate", tokens),
	}
	if !timeGateExcluded {
		return err
	}
	return &timeGateExcludedAuthError{cause: err}
}

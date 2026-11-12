package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
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

// routingContextEntriesForAuth returns the model routeModel resolves to for
// auth and the model entries of auth's OpenAI-compatible provider, or no
// entries when auth is not a config-backed OpenAI-compatible credential.
func (m *Manager) routingContextEntriesForAuth(auth *Auth, routeModel string, routing *apiKeyModelRoutingSnapshot) (string, []modelAliasEntry) {
	if m == nil || auth == nil || !isConfiguredOpenAICompatAuth(auth) {
		return "", nil
	}
	requestedModel := strings.TrimSpace(rewriteModelForAuth(routeModel, auth))
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(routeModel)
	}
	if requestedModel == "" {
		return "", nil
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if routing == nil {
		routing = m.loadAPIKeyModelRouting()
	}
	entry := resolveOpenAICompatConfigForAuth(routing.config, auth, providerKey, compatName)
	if entry == nil {
		return "", nil
	}
	return requestedModel, asModelAliasEntries(entry.Models)
}

// routingContextLimitForAuth returns the routing-max-context-length configured
// on the OpenAI-compatible model entry auth serves routeModel through; 0 means
// unlimited. Callers must hold at least m.mu.RLock while auth comes from m.auths.
func (m *Manager) routingContextLimitForAuth(auth *Auth, routeModel string) int {
	requestedModel, entries := m.routingContextEntriesForAuth(auth, routeModel, nil)
	return routingContextLimitForModels(requestedModel, entries)
}

// filterExecutionModelsByRoutingContext drops upstream models whose own
// routing-max-context-length is below the request's estimated input tokens.
// Selection keeps an auth while any member of its alias pool can hold the
// request; this keeps the pool rotation from sending the request to a smaller
// member. An emptied list makes the caller move on to the next auth. routing
// is the snapshot the models were built from, so a concurrent reload cannot
// leave a capped member unmatched; nil loads the current one.
func (m *Manager) filterExecutionModelsByRoutingContext(auth *Auth, routeModel string, models []string, opts cliproxyexecutor.Options, routing *apiKeyModelRoutingSnapshot) []string {
	tokens, ok := estimatedInputTokensFromMetadata(opts.Metadata)
	if !ok || len(models) == 0 {
		return models
	}
	requestedModel, entries := m.routingContextEntriesForAuth(auth, routeModel, routing)
	if len(entries) == 0 {
		return models
	}
	_, candidates := modelAliasLookupCandidates(requestedModel)
	// An upstream listed more than once follows the selection rule: any entry
	// without a limit makes it unlimited, otherwise the largest limit applies.
	limits := make(map[string]int, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.GetName())
		if name == "" {
			continue
		}
		alias := strings.TrimSpace(entry.GetAlias())
		matched := false
		for _, candidate := range candidates {
			if candidate != "" && (strings.EqualFold(alias, candidate) || strings.EqualFold(name, candidate)) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		key := routingContextModelKey(name)
		limit := routingContextLimitOf(entry)
		if previous, seen := limits[key]; seen && (previous == 0 || limit == 0) {
			limits[key] = 0
			continue
		}
		limits[key] = max(limits[key], limit)
	}
	out := make([]string, 0, len(models))
	for _, model := range models {
		if limit := limits[routingContextModelKey(model)]; limit > 0 && tokens > limit {
			continue
		}
		out = append(out, model)
	}
	return out
}

// routingContextModelKey compares configured and resolved upstream names
// without their thinking suffix, e.g. "foo(8192)" and "foo".
func routingContextModelKey(model string) string {
	base := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if base == "" {
		base = strings.TrimSpace(model)
	}
	return strings.ToLower(base)
}

// authExceedsRoutingContextLimit reports whether the request's estimated input
// tokens exceed the largest input ceiling auth can hold for routeModel: its
// configured routing-max-context-length when set, otherwise the context window
// the registry catalog declares for the model. Requests without an estimate are
// never excluded.
func (m *Manager) authExceedsRoutingContextLimit(auth *Auth, routeModel string, opts cliproxyexecutor.Options) (tokens int, limit int, exceeded bool) {
	tokens, ok := estimatedInputTokensFromMetadata(opts.Metadata)
	if !ok {
		return 0, 0, false
	}
	limit = m.routingContextLimitForAuth(auth, routeModel)
	if limit <= 0 {
		// Native providers (claude, gemini, codex, ...) have no
		// routing-max-context-length, so their catalog window is the only
		// ceiling available. Without it an oversized prompt reaches the
		// upstream, which rejects it after the whole upload.
		limit = registeredModelContextWindow(auth, routeModel)
	}
	return tokens, limit, limit > 0 && tokens > limit
}

// registeredModelContextWindow returns the context window the registry catalog
// declares for model, or 0 when the model is unknown or the entry belongs to a
// different provider family than auth. Comparing families keeps an OAuth alias
// that happens to spell another model's ID from inheriting that model's window.
func registeredModelContextWindow(auth *Auth, model string) int {
	if auth == nil {
		// Without an auth there is no provider family to validate the catalog
		// entry against, and a bare model ID may name another family's model.
		return 0
	}
	family := strings.ToLower(strings.TrimSpace(auth.Provider))
	name := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if name == "" {
		return 0
	}
	info := registry.LookupModelInfo(name, family)
	if info == nil || (info.ContextLength <= 0 && info.InputTokenLimit <= 0) {
		// A client-registered entry carries whatever the provider's /models
		// call reported and may omit the window entirely; the static catalog
		// still knows it.
		if static := registry.LookupStaticModelInfoForProvider(name, family); static != nil {
			info = static
		}
	}
	if info == nil {
		return 0
	}
	if family != "" && info.Type != "" && !strings.EqualFold(info.Type, family) {
		return 0
	}
	if info.ContextLength > 0 {
		return info.ContextLength
	}
	return info.InputTokenLimit
}

// contextOverflowMarkers are the upstream phrasings for "the prompt itself is
// larger than the model's input window". They describe the request, not the
// credential, so retrying the same payload on another credential cannot work.
var contextOverflowMarkers = []string{
	"prompt is too long",
	"prompt_too_long",
	"context_length_exceeded",
	"context length exceeded",
	"context_too_large",
	"maximum context length",
}

// isContextOverflowError reports whether err says the prompt exceeds the model
// window. Both the error code and the message are checked because Anthropic
// reports the overflow as invalid_request_error with a message only.
func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		switch strings.ToLower(strings.TrimSpace(authErr.Code)) {
		case "context_length_exceeded", "prompt_too_long", "context_too_large":
			return true
		}
	}
	message := strings.ToLower(err.Error())
	for _, marker := range contextOverflowMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
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

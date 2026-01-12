package auth

import (
	"context"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// PrioritySelector selects credentials based on provider priority.
// It tries providers in priority order, using an inner selector for each provider.
type PrioritySelector struct {
	// ProviderPriority maps model names to ordered provider lists.
	// Model-specific priority takes precedence over ProviderOrder.
	ProviderPriority map[string][]string

	// ProviderOrder is the global default provider order.
	// Used when a model doesn't have a specific ProviderPriority entry.
	ProviderOrder []string

	// InnerSelector is used to select among auths within each provider.
	// If nil, defaults to RoundRobinSelector behavior.
	InnerSelector Selector

	// Mode is passed to inner selector if needed.
	Mode string
}

// Pick selects the next available auth based on provider priority.
// Algorithm:
//  1. Get priority list for model (model-specific or global)
//  2. For each provider in priority order:
//     a. Filter auths to only those matching the provider
//     b. Try to pick an auth using inner selector
//     c. If successful, return it
//     d. If all blocked (modelCooldownError), track earliest reset and continue
//  3. If all providers exhausted, return modelCooldownError with earliest reset
func (s *PrioritySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	// Get priority list for this model
	priorityList := s.getPriorityList(model)

	// If no priority list defined, fall back to default behavior
	if len(priorityList) == 0 {
		return s.getInnerSelector().Pick(ctx, provider, model, opts, auths)
	}

	// Group auths by provider
	authsByProvider := groupAuthsByProvider(auths)

	var earliestReset time.Time
	var cooldownCount int

	// Try each provider in priority order
	for _, providerName := range priorityList {
		providerAuths, ok := authsByProvider[providerName]
		if !ok || len(providerAuths) == 0 {
			continue // Skip providers with no auths
		}

		auth, err := s.getInnerSelector().Pick(ctx, providerName, model, opts, providerAuths)
		if err == nil {
			return auth, nil
		}

		// If it's a cooldown error, track it and continue to next provider
		if cooldownErr, ok := err.(*modelCooldownError); ok {
			cooldownCount++
			resetTime := time.Now().Add(cooldownErr.resetIn)
			if earliestReset.IsZero() || resetTime.Before(earliestReset) {
				earliestReset = resetTime
			}
			continue
		}

		// For other errors, continue to next provider
		continue
	}

	// All providers exhausted
	if cooldownCount > 0 && !earliestReset.IsZero() {
		resetIn := time.Until(earliestReset)
		if resetIn < 0 {
			resetIn = 0
		}
		return nil, newModelCooldownError(model, "", resetIn)
	}

	return nil, &Error{Code: "auth_not_found", Message: "no auth available for any provider in priority list"}
}

func (s *PrioritySelector) getPriorityList(model string) []string {
	// Check model-specific priority first
	if s.ProviderPriority != nil {
		if priority, ok := s.ProviderPriority[model]; ok && len(priority) > 0 {
			return priority
		}
	}
	// Fall back to global order
	return s.ProviderOrder
}

func (s *PrioritySelector) getInnerSelector() Selector {
	if s.InnerSelector != nil {
		return s.InnerSelector
	}
	return &RoundRobinSelector{Mode: s.Mode}
}

func groupAuthsByProvider(auths []*Auth) map[string][]*Auth {
	result := make(map[string][]*Auth)
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		result[auth.Provider] = append(result[auth.Provider], auth)
	}
	return result
}

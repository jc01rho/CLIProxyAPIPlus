package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// quotaCooldownDisabledForAuthWithConfig reports whether quota cooling is disabled
// for the given auth, taking into account per-auth overrides, provider-level config,
// global config, and the process-wide kill switch.
func quotaCooldownDisabledForAuthWithConfig(auth *Auth, cfg *internalconfig.Config) bool {
	if auth != nil {
		if override, ok := auth.DisableCoolingOverride(); ok {
			return override
		}
		if providerCoolingDisabledForAuth(auth, cfg) {
			return true
		}
	}
	if cfg != nil && cfg.DisableCooling {
		return true
	}
	return quotaCooldownDisabled.Load()
}

// providerCoolingDisabledForAuth checks whether the OpenAI-compat provider entry
// associated with the auth has explicitly disabled cooling.
func providerCoolingDisabledForAuth(auth *Auth, cfg *internalconfig.Config) bool {
	if auth == nil || cfg == nil {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider == "" {
		return false
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if providerKey == "" && compatName == "" && provider != "openai-compatibility" {
		return false
	}
	if providerKey == "" {
		providerKey = provider
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, provider)
	return entry != nil && entry.DisableCooling
}

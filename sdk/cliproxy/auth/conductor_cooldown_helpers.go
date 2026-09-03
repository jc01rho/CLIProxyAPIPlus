package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// quotaCooldownDisabledForAuthWithConfig reports whether quota cooling is disabled
// for the given auth, taking into account per-auth overrides, provider-level config,
// global config, Home-owned cooldown, and the process-wide kill switch.
func quotaCooldownDisabledForAuthWithConfig(auth *Auth, cfg *internalconfig.Config) bool {
	// Home owns cooldown state, so downstream instances must not schedule local cooldowns.
	if cfg != nil && cfg.Home.Enabled {
		return true
	}
	if auth != nil {
		if override, ok := auth.DisableCoolingOverride(); ok {
			return override
		}
		if override, ok := providerCoolingOverrideForAuth(auth, cfg); ok {
			return override
		}
	}
	if cfg != nil && cfg.DisableCooling {
		return true
	}
	return quotaCooldownDisabled.Load()
}

// providerCoolingOverrideForAuth reports an explicit OpenAI-compat provider
// disable-cooling override when the matching entry sets the optional bool.
// QuotaCooldownDisabledForAuth returns whether cooling is disabled for the auth under global settings.
func QuotaCooldownDisabledForAuth(auth *Auth) bool {
	return quotaCooldownDisabledForAuth(auth)
}

// QuotaCooldownDisabledForAuthWithConfig returns whether cooling is disabled for the auth with the given config.
func QuotaCooldownDisabledForAuthWithConfig(auth *Auth, cfg *internalconfig.Config) bool {
	return quotaCooldownDisabledForAuthWithConfig(auth, cfg)
}

func providerCoolingOverrideForAuth(auth *Auth, cfg *internalconfig.Config) (bool, bool) {
	if auth == nil || cfg == nil {
		return false, false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider == "" {
		return false, false
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if providerKey == "" && compatName == "" && provider != "openai-compatibility" {
		return false, false
	}
	if providerKey == "" {
		providerKey = provider
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, provider)
	if entry == nil || entry.DisableCooling == nil {
		return false, false
	}
	return *entry.DisableCooling, true
}

// providerCoolingDisabledForAuth reports whether the matching OpenAI-compat
// provider entry explicitly disabled cooling.
func providerCoolingDisabledForAuth(auth *Auth, cfg *internalconfig.Config) bool {
	override, ok := providerCoolingOverrideForAuth(auth, cfg)
	return ok && override
}

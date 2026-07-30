package cliproxy

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type nativeAPIKeyConfig interface {
	GetAPIKey() string
	GetBaseURL() string
	GetPrefix() string
	GetProxyURL() string
}

type nativeConfiguredModel interface {
	GetName() string
	GetAlias() string
	GetDisplayName() string
}

type nativeConfiguredModelAdapter[T nativeConfiguredModel] struct {
	model T
}

func (a nativeConfiguredModelAdapter[T]) GetName() string        { return a.model.GetName() }
func (a nativeConfiguredModelAdapter[T]) GetAlias() string       { return a.model.GetAlias() }
func (a nativeConfiguredModelAdapter[T]) GetDisplayName() string { return a.model.GetDisplayName() }
func (a nativeConfiguredModelAdapter[T]) GetThinking() *registry.ThinkingSupport {
	return nil
}

func buildNativeConfigModels[T nativeConfiguredModel](models []T, ownedBy, modelType string) []*ModelInfo {
	adapted := make([]nativeConfiguredModelAdapter[T], 0, len(models))
	for _, model := range models {
		adapted = append(adapted, nativeConfiguredModelAdapter[T]{model: model})
	}
	return buildConfigModels(adapted, ownedBy, modelType)
}

func resolveNativeAPIKeyConfig[T nativeAPIKeyConfig](entries []T, auth *coreauth.Auth) *T {
	if auth == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes[coreauth.AttributeAPIKey])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	matchesCredentials := func(entry T) bool {
		configKey := strings.TrimSpace(entry.GetAPIKey())
		configBase := strings.TrimSpace(entry.GetBaseURL())
		if attrKey != "" && attrBase != "" {
			return strings.EqualFold(configKey, attrKey) && strings.EqualFold(configBase, attrBase)
		}
		if attrKey != "" {
			return strings.EqualFold(configKey, attrKey) && (configBase == "" || strings.EqualFold(configBase, attrBase))
		}
		return attrBase != "" && strings.EqualFold(configBase, attrBase)
	}
	if entry := configEntryForAuthIndex(auth, entries); entry != nil && matchesCredentials(*entry) {
		return entry
	}
	for i := range entries {
		entry := entries[i]
		if matchesCredentials(entry) && strings.EqualFold(strings.TrimSpace(entry.GetPrefix()), strings.TrimSpace(auth.Prefix)) && strings.EqualFold(strings.TrimSpace(entry.GetProxyURL()), strings.TrimSpace(auth.ProxyURL)) {
			return &entries[i]
		}
	}
	for i := range entries {
		if matchesCredentials(entries[i]) {
			return &entries[i]
		}
	}
	return nil
}

func (s *Service) resolveConfigCommandCodeKey(auth *coreauth.Auth) *config.CommandCodeKey {
	if s == nil || s.cfg == nil {
		return nil
	}
	return resolveNativeAPIKeyConfig(s.cfg.CommandCodeKey, auth)
}

func (s *Service) resolveConfigMistralKey(auth *coreauth.Auth) *config.MistralKey {
	if s == nil || s.cfg == nil {
		return nil
	}
	return resolveNativeAPIKeyConfig(s.cfg.MistralKey, auth)
}

func buildCommandCodeConfigModels(entry *config.CommandCodeKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildNativeConfigModels(entry.Models, "commandcode", "commandcode")
}

func buildMistralConfigModels(entry *config.MistralKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildNativeConfigModels(entry.Models, "mistral", "mistral")
}

package config

import "strings"

type legacyConfigData struct {
	GeminiKeys          *[]string                   `yaml:"generative-language-api-key"`
	OpenAICompatibility []legacyOpenAICompatibility `yaml:"openai-compatibility"`
	AmpUpstreamURL      *string                     `yaml:"amp-upstream-url"`
	AmpUpstreamAPIKey   *string                     `yaml:"amp-upstream-api-key"`
	AmpRestrictLocal    *bool                       `yaml:"amp-restrict-management-to-localhost"`
	AmpModelMappings    *[]AmpModelMapping          `yaml:"amp-model-mappings"`
	AmpCode             legacyAmpCode               `yaml:"ampcode"`
}

type legacyOpenAICompatibility struct {
	Name    string    `yaml:"name"`
	BaseURL string    `yaml:"base-url"`
	APIKeys *[]string `yaml:"api-keys"`
}

type legacyAmpCode struct {
	RestrictLocal *bool `yaml:"restrict-management-to-localhost"`
}

func (cfg *Config) migrateLegacyConfig(legacy legacyConfigData) bool {
	if cfg == nil {
		return false
	}

	present := legacy.GeminiKeys != nil || legacy.AmpUpstreamURL != nil ||
		legacy.AmpUpstreamAPIKey != nil || legacy.AmpRestrictLocal != nil ||
		legacy.AmpModelMappings != nil

	if legacy.GeminiKeys != nil {
		seen := make(map[string]struct{}, len(cfg.GeminiKey))
		for _, entry := range cfg.GeminiKey {
			seen[strings.TrimSpace(entry.APIKey)] = struct{}{}
		}
		for _, rawKey := range *legacy.GeminiKeys {
			key := strings.TrimSpace(rawKey)
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			cfg.GeminiKey = append(cfg.GeminiKey, GeminiKey{APIKey: key})
			seen[key] = struct{}{}
		}
	}

	for _, legacyProvider := range legacy.OpenAICompatibility {
		if legacyProvider.APIKeys == nil {
			continue
		}
		present = true
		target := findOpenAICompatibility(cfg.OpenAICompatibility, legacyProvider.Name, legacyProvider.BaseURL)
		if target == nil {
			continue
		}
		seen := make(map[string]struct{}, len(target.APIKeyEntries))
		for _, entry := range target.APIKeyEntries {
			seen[strings.TrimSpace(entry.APIKey)] = struct{}{}
		}
		for _, rawKey := range *legacyProvider.APIKeys {
			key := strings.TrimSpace(rawKey)
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			target.APIKeyEntries = append(target.APIKeyEntries, OpenAICompatibilityAPIKey{APIKey: key})
			seen[key] = struct{}{}
		}
	}

	if cfg.AmpCode.UpstreamURL == "" && legacy.AmpUpstreamURL != nil {
		cfg.AmpCode.UpstreamURL = strings.TrimSpace(*legacy.AmpUpstreamURL)
	}
	if cfg.AmpCode.UpstreamAPIKey == "" && legacy.AmpUpstreamAPIKey != nil {
		cfg.AmpCode.UpstreamAPIKey = strings.TrimSpace(*legacy.AmpUpstreamAPIKey)
	}
	if legacy.AmpRestrictLocal != nil && legacy.AmpCode.RestrictLocal == nil {
		cfg.AmpCode.RestrictManagementToLocalhost = *legacy.AmpRestrictLocal
	}
	if len(cfg.AmpCode.ModelMappings) == 0 && legacy.AmpModelMappings != nil {
		cfg.AmpCode.ModelMappings = append([]AmpModelMapping(nil), (*legacy.AmpModelMappings)...)
	}

	return present
}

func findOpenAICompatibility(entries []OpenAICompatibility, name, baseURL string) *OpenAICompatibility {
	name = strings.ToLower(strings.TrimSpace(name))
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	for i := range entries {
		if strings.ToLower(strings.TrimSpace(entries[i].Name)) == name &&
			strings.ToLower(strings.TrimSpace(entries[i].BaseURL)) == baseURL {
			return &entries[i]
		}
	}
	return nil
}

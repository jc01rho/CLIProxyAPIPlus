package config

import "strings"

// APIKeyIPBlacklistConfig configures automatic blocking for repeated invalid API keys.
type APIKeyIPBlacklistConfig struct {
	FailureThreshold int    `yaml:"failure-threshold,omitempty" json:"failure-threshold,omitempty"`
	FailureWindow    string `yaml:"failure-window,omitempty" json:"failure-window,omitempty"`
	BlockDuration    string `yaml:"block-duration,omitempty" json:"block-duration,omitempty"`
}

// OAuthEndpointConfig overrides provider OAuth and API endpoints.
type OAuthEndpointConfig struct {
	ApiBaseURL         string `yaml:"api-base-url,omitempty" json:"api-base-url,omitempty"`
	AuthorizeURL       string `yaml:"authorize-url,omitempty" json:"authorize-url,omitempty"`
	TokenURL           string `yaml:"token-url,omitempty" json:"token-url,omitempty"`
	RefreshURL         string `yaml:"refresh-url,omitempty" json:"refresh-url,omitempty"`
	UserinfoURL        string `yaml:"userinfo-url,omitempty" json:"userinfo-url,omitempty"`
	DeviceAuthorizeURL string `yaml:"device-authorize-url,omitempty" json:"device-authorize-url,omitempty"`
}

func (c *OAuthEndpointConfig) ApplyDefaults(defaults OAuthEndpointConfig) OAuthEndpointConfig {
	result := *c
	if result.ApiBaseURL == "" {
		result.ApiBaseURL = defaults.ApiBaseURL
	}
	if result.AuthorizeURL == "" {
		result.AuthorizeURL = defaults.AuthorizeURL
	}
	if result.TokenURL == "" {
		result.TokenURL = defaults.TokenURL
	}
	if result.RefreshURL == "" {
		result.RefreshURL = defaults.RefreshURL
	}
	if result.UserinfoURL == "" {
		result.UserinfoURL = defaults.UserinfoURL
	}
	if result.DeviceAuthorizeURL == "" {
		result.DeviceAuthorizeURL = defaults.DeviceAuthorizeURL
	}
	return result
}

func (cfg *Config) NormalizeOAuthEndpointOverrides() {
	if cfg == nil || len(cfg.OAuthEndpointOverrides) == 0 {
		return
	}
	normalized := make(map[string]OAuthEndpointConfig, len(cfg.OAuthEndpointOverrides))
	for provider, ep := range cfg.OAuthEndpointOverrides {
		normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
		if normalizedProvider == "" {
			continue
		}
		ep.ApiBaseURL = strings.TrimSpace(ep.ApiBaseURL)
		ep.AuthorizeURL = strings.TrimSpace(ep.AuthorizeURL)
		ep.TokenURL = strings.TrimSpace(ep.TokenURL)
		ep.RefreshURL = strings.TrimSpace(ep.RefreshURL)
		ep.UserinfoURL = strings.TrimSpace(ep.UserinfoURL)
		ep.DeviceAuthorizeURL = strings.TrimSpace(ep.DeviceAuthorizeURL)
		normalized[normalizedProvider] = ep
	}
	cfg.OAuthEndpointOverrides = normalized
}

// AmpModelMapping maps an Amp-requested model to an available model.
type AmpModelMapping struct {
	From  string `yaml:"from" json:"from"`
	To    string `yaml:"to" json:"to"`
	Regex bool   `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// AmpCode groups Amp CLI integration settings.
type AmpCode struct {
	UpstreamURL                   string                   `yaml:"upstream-url" json:"upstream-url"`
	UpstreamAPIKey                string                   `yaml:"upstream-api-key" json:"upstream-api-key"`
	UpstreamAPIKeys               []AmpUpstreamAPIKeyEntry `yaml:"upstream-api-keys,omitempty" json:"upstream-api-keys,omitempty"`
	RestrictManagementToLocalhost bool                     `yaml:"restrict-management-to-localhost" json:"restrict-management-to-localhost"`
	ModelMappings                 []AmpModelMapping        `yaml:"model-mappings" json:"model-mappings"`
	ForceModelMappings            bool                     `yaml:"force-model-mappings" json:"force-model-mappings"`
}

// AmpUpstreamAPIKeyEntry maps client API keys to an Amp upstream key.
type AmpUpstreamAPIKeyEntry struct {
	UpstreamAPIKey string   `yaml:"upstream-api-key" json:"upstream-api-key"`
	APIKeys        []string `yaml:"api-keys" json:"api-keys"`
}

// KiroKey represents Kiro authentication settings.
type KiroKey struct {
	TokenFile         string `yaml:"token-file,omitempty" json:"token-file,omitempty"`
	AccessToken       string `yaml:"access-token,omitempty" json:"access-token,omitempty"`
	Comment           string `yaml:"comment,omitempty" json:"comment,omitempty"`
	RefreshToken      string `yaml:"refresh-token,omitempty" json:"refresh-token,omitempty"`
	ProfileArn        string `yaml:"profile-arn,omitempty" json:"profile-arn,omitempty"`
	Region            string `yaml:"region,omitempty" json:"region,omitempty"`
	APIRegion         string `yaml:"api-region,omitempty" json:"api-region,omitempty"`
	StartURL          string `yaml:"start-url,omitempty" json:"start-url,omitempty"`
	ProxyURL          string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
	AgentTaskType     string `yaml:"agent-task-type,omitempty" json:"agent-task-type,omitempty"`
	PreferredEndpoint string `yaml:"preferred-endpoint,omitempty" json:"preferred-endpoint,omitempty"`
}

// KiroFingerprintConfig fixes the client fingerprint used for Kiro requests.
type KiroFingerprintConfig struct {
	OIDCSDKVersion      string `yaml:"oidc-sdk-version,omitempty" json:"oidc-sdk-version,omitempty"`
	RuntimeSDKVersion   string `yaml:"runtime-sdk-version,omitempty" json:"runtime-sdk-version,omitempty"`
	StreamingSDKVersion string `yaml:"streaming-sdk-version,omitempty" json:"streaming-sdk-version,omitempty"`
	OSType              string `yaml:"os-type,omitempty" json:"os-type,omitempty"`
	OSVersion           string `yaml:"os-version,omitempty" json:"os-version,omitempty"`
	NodeVersion         string `yaml:"node-version,omitempty" json:"node-version,omitempty"`
	KiroVersion         string `yaml:"kiro-version,omitempty" json:"kiro-version,omitempty"`
	KiroHash            string `yaml:"kiro-hash,omitempty" json:"kiro-hash,omitempty"`
}

func (cfg *Config) GetOAuthEndpointOverride(provider string) OAuthEndpointConfig {
	if cfg == nil {
		return OAuthEndpointConfig{}
	}
	return cfg.OAuthEndpointOverrides[strings.ToLower(strings.TrimSpace(provider))]
}

// FoldCommandCodeLegacyAPIKey makes api-key-entries the single source of truth
// when any nested keys are present. A duplicated top-level api-key is dropped;
// a unique top-level api-key is prepended as an entry so synthesizer does not
// silently ignore it.
func FoldCommandCodeLegacyAPIKey(entry *CommandCodeKey) {
	if entry == nil {
		return
	}
	legacy := strings.TrimSpace(entry.APIKey)
	if legacy == "" || len(entry.APIKeyEntries) == 0 {
		return
	}
	for i := range entry.APIKeyEntries {
		if strings.TrimSpace(entry.APIKeyEntries[i].APIKey) == legacy {
			entry.APIKey = ""
			return
		}
	}
	folded := OpenAICompatibilityAPIKey{
		APIKey:   legacy,
		ProxyURL: strings.TrimSpace(entry.ProxyURL),
	}
	entry.APIKeyEntries = append([]OpenAICompatibilityAPIKey{folded}, entry.APIKeyEntries...)
	entry.APIKey = ""
}

func (cfg *Config) SanitizeCommandCodeKeys() {
	if cfg == nil {
		return
	}
	out := make([]CommandCodeKey, 0, len(cfg.CommandCodeKey))
	for _, entry := range cfg.CommandCodeKey {
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		nested := make([]OpenAICompatibilityAPIKey, 0, len(entry.APIKeyEntries))
		for _, apiKeyEntry := range entry.APIKeyEntries {
			apiKeyEntry.APIKey = strings.TrimSpace(apiKeyEntry.APIKey)
			apiKeyEntry.ProxyURL = strings.TrimSpace(apiKeyEntry.ProxyURL)
			apiKeyEntry.Comment = strings.TrimSpace(apiKeyEntry.Comment)
			if apiKeyEntry.APIKey != "" {
				nested = append(nested, apiKeyEntry)
			}
		}
		entry.APIKeyEntries = nested
		FoldCommandCodeLegacyAPIKey(&entry)
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.BillingClass = normalizeBillingClass(entry.BillingClass)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		for i := range entry.Models {
			entry.Models[i].Name = strings.TrimSpace(entry.Models[i].Name)
			entry.Models[i].Alias = strings.TrimSpace(entry.Models[i].Alias)
		}
		if entry.APIKey != "" || len(entry.APIKeyEntries) > 0 {
			out = append(out, entry)
		}
	}
	cfg.CommandCodeKey = out
}

// FoldFreebuffLegacyAPIKey makes api-key-entries the single source of truth.
func FoldFreebuffLegacyAPIKey(entry *FreebuffKey) {
	if entry == nil {
		return
	}
	legacy := strings.TrimSpace(entry.APIKey)
	if legacy == "" || len(entry.APIKeyEntries) == 0 {
		return
	}
	for i := range entry.APIKeyEntries {
		if strings.TrimSpace(entry.APIKeyEntries[i].APIKey) == legacy {
			entry.APIKey = ""
			return
		}
	}
	entry.APIKeyEntries = append([]OpenAICompatibilityAPIKey{{
		APIKey:   legacy,
		ProxyURL: strings.TrimSpace(entry.ProxyURL),
	}}, entry.APIKeyEntries...)
	entry.APIKey = ""
}

func (cfg *Config) SanitizeFreebuffKeys() {
	if cfg == nil {
		return
	}
	out := make([]FreebuffKey, 0, len(cfg.FreebuffKey))
	for _, entry := range cfg.FreebuffKey {
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		nested := make([]OpenAICompatibilityAPIKey, 0, len(entry.APIKeyEntries))
		for _, apiKeyEntry := range entry.APIKeyEntries {
			apiKeyEntry.APIKey = strings.TrimSpace(apiKeyEntry.APIKey)
			apiKeyEntry.ProxyURL = strings.TrimSpace(apiKeyEntry.ProxyURL)
			apiKeyEntry.Comment = strings.TrimSpace(apiKeyEntry.Comment)
			if apiKeyEntry.APIKey != "" {
				nested = append(nested, apiKeyEntry)
			}
		}
		entry.APIKeyEntries = nested
		FoldFreebuffLegacyAPIKey(&entry)
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.BillingClass = normalizeBillingClass(entry.BillingClass)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		for i := range entry.Models {
			entry.Models[i].Name = strings.TrimSpace(entry.Models[i].Name)
			entry.Models[i].Alias = strings.TrimSpace(entry.Models[i].Alias)
			entry.Models[i].AgentID = strings.TrimSpace(entry.Models[i].AgentID)
		}
		if entry.APIKey != "" || len(entry.APIKeyEntries) > 0 {
			out = append(out, entry)
		}
	}
	cfg.FreebuffKey = out
}

func (cfg *Config) SanitizeMistralKeys() {
	if cfg == nil {
		return
	}
	out := make([]MistralKey, 0, len(cfg.MistralKey))
	for _, entry := range cfg.MistralKey {
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.BillingClass = normalizeBillingClass(entry.BillingClass)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		if entry.APIKey != "" {
			out = append(out, entry)
		}
	}
	cfg.MistralKey = out
}

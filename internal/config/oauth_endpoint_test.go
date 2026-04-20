package config

import (
	"testing"
)

func TestOAuthEndpointConfig_ApplyDefaults(t *testing.T) {
	defaults := OAuthEndpointConfig{
		ApiBaseURL:         "https://default.api",
		AuthorizeURL:       "https://default.auth",
		TokenURL:           "https://default.token",
		RefreshURL:         "https://default.refresh",
		UserinfoURL:        "https://default.userinfo",
		DeviceAuthorizeURL: "https://default.device",
	}

	tests := []struct {
		name     string
		input    OAuthEndpointConfig
		expected OAuthEndpointConfig
	}{
		{
			name:     "empty input uses all defaults",
			input:    OAuthEndpointConfig{},
			expected: defaults,
		},
		{
			name: "partial override",
			input: OAuthEndpointConfig{
				TokenURL:    "https://custom.token",
				RefreshURL:  "https://custom.refresh",
			},
			expected: OAuthEndpointConfig{
				ApiBaseURL:         "https://default.api",
				AuthorizeURL:       "https://default.auth",
				TokenURL:           "https://custom.token",
				RefreshURL:         "https://custom.refresh",
				UserinfoURL:        "https://default.userinfo",
				DeviceAuthorizeURL: "https://default.device",
			},
		},
		{
			name: "full override",
			input: OAuthEndpointConfig{
				ApiBaseURL:         "https://custom.api",
				AuthorizeURL:       "https://custom.auth",
				TokenURL:           "https://custom.token",
				RefreshURL:         "https://custom.refresh",
				UserinfoURL:        "https://custom.userinfo",
				DeviceAuthorizeURL: "https://custom.device",
			},
			expected: OAuthEndpointConfig{
				ApiBaseURL:         "https://custom.api",
				AuthorizeURL:       "https://custom.auth",
				TokenURL:           "https://custom.token",
				RefreshURL:         "https://custom.refresh",
				UserinfoURL:        "https://custom.userinfo",
				DeviceAuthorizeURL: "https://custom.device",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.input.ApplyDefaults(defaults)
			if result != tt.expected {
				t.Errorf("ApplyDefaults() = %+v, want %+v", result, tt.expected)
			}
		})
	}
}

func TestNormalizeOAuthEndpointOverrides(t *testing.T) {
	cfg := &Config{
		OAuthEndpointOverrides: map[string]OAuthEndpointConfig{
			"ANTIGRAVITY": {TokenURL: "https://custom.token", AuthorizeURL: "  https://custom.auth  "},
			"  claude  ":  {TokenURL: "https://claude.token"},
			"":            {TokenURL: "should be dropped"},
		},
	}

	cfg.NormalizeOAuthEndpointOverrides()

	if len(cfg.OAuthEndpointOverrides) != 2 {
		t.Errorf("expected 2 providers, got %d", len(cfg.OAuthEndpointOverrides))
	}

	if ep, ok := cfg.OAuthEndpointOverrides["antigravity"]; !ok {
		t.Error("expected 'antigravity' key (normalized from 'ANTIGRAVITY')")
	} else if ep.AuthorizeURL != "https://custom.auth" {
		t.Errorf("expected trimmed AuthorizeURL, got %q", ep.AuthorizeURL)
	}

	if _, ok := cfg.OAuthEndpointOverrides[""]; ok {
		t.Error("empty provider key should be dropped")
	}
}

func TestGetOAuthEndpointOverride(t *testing.T) {
	cfg := &Config{
		OAuthEndpointOverrides: map[string]OAuthEndpointConfig{
			"antigravity": {TokenURL: "https://antigravity.token"},
		},
	}

	tests := []struct {
		name     string
		provider string
		expected OAuthEndpointConfig
	}{
		{
			name:     "existing provider",
			provider: "antigravity",
			expected: OAuthEndpointConfig{TokenURL: "https://antigravity.token"},
		},
		{
			name:     "existing provider case insensitive",
			provider: "ANTIGRAVITY",
			expected: OAuthEndpointConfig{TokenURL: "https://antigravity.token"},
		},
		{
			name:     "non-existing provider",
			provider: "nonexistent",
			expected: OAuthEndpointConfig{},
		},
		{
			name:     "empty provider",
			provider: "",
			expected: OAuthEndpointConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cfg.GetOAuthEndpointOverride(tt.provider)
			if result != tt.expected {
				t.Errorf("GetOAuthEndpointOverride(%q) = %+v, want %+v", tt.provider, result, tt.expected)
			}
		})
	}
}

func TestGetOAuthEndpointOverride_NilConfig(t *testing.T) {
	var cfg *Config
	result := cfg.GetOAuthEndpointOverride("antigravity")
	if result != (OAuthEndpointConfig{}) {
		t.Errorf("nil config should return empty config, got %+v", result)
	}
}

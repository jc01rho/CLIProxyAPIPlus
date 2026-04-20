package config

import (
	"testing"
)

func TestOAuthEndpointConfig_RefreshURLDefaultsToTokenURL(t *testing.T) {
	tests := []struct {
		name      string
		override  OAuthEndpointConfig
		forRefresh bool
		wantToken string
		wantRefresh string
	}{
		{
			name:      "token exchange uses TokenURL override",
			override:  OAuthEndpointConfig{TokenURL: "https://custom.token", RefreshURL: "https://custom.refresh"},
			forRefresh: false,
			wantToken:  "https://custom.token",
			wantRefresh: "https://custom.refresh",
		},
		{
			name:      "refresh uses RefreshURL override",
			override:  OAuthEndpointConfig{TokenURL: "https://custom.token", RefreshURL: "https://custom.refresh"},
			forRefresh: true,
			wantToken:  "https://custom.token",
			wantRefresh: "https://custom.refresh",
		},
		{
			name:      "refresh falls back to TokenURL when RefreshURL empty",
			override:  OAuthEndpointConfig{TokenURL: "https://custom.token"},
			forRefresh: true,
			wantToken:  "https://custom.token",
			wantRefresh: "https://custom.token",
		},
		{
			name:      "empty override returns empty for all fields",
			override:  OAuthEndpointConfig{},
			forRefresh: false,
			wantToken:  "",
			wantRefresh: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.override.TokenURL != tt.wantToken {
				t.Errorf("TokenURL = %q, want %q", tt.override.TokenURL, tt.wantToken)
			}
			if tt.forRefresh && tt.override.RefreshURL == "" && tt.override.TokenURL != "" {
				if tt.override.TokenURL != tt.wantRefresh {
					t.Errorf("refresh should default to TokenURL when RefreshURL empty, got %q, want %q", tt.override.TokenURL, tt.wantRefresh)
				}
			}
			if !tt.forRefresh && tt.override.TokenURL != "" {
				if tt.override.RefreshURL != "" && tt.override.RefreshURL == tt.wantToken {
					t.Errorf("token exchange should not use RefreshURL, but both are %q", tt.override.RefreshURL)
				}
			}
		})
	}
}

func TestGetOAuthEndpointOverride_AllProviderKeys(t *testing.T) {
	providers := []string{
		"antigravity", "claude", "codex", "gemini",
		"github-copilot", "kiro", "kimi",
	}

	cfg := &Config{
		OAuthEndpointOverrides: map[string]OAuthEndpointConfig{
			"antigravity":    {TokenURL: "https://antigravity.token"},
			"claude":         {TokenURL: "https://claude.token"},
			"codex":          {TokenURL: "https://codex.token"},
			"gemini":         {TokenURL: "https://gemini.token"},
			"github-copilot": {TokenURL: "https://copilot.token"},
			"kiro":           {TokenURL: "https://kiro.token"},
			"kimi":           {TokenURL: "https://kimi.token"},
		},
	}

	for _, provider := range providers {
		ep := cfg.GetOAuthEndpointOverride(provider)
		if ep.TokenURL == "" {
			t.Errorf("GetOAuthEndpointOverride(%q) returned empty TokenURL", provider)
		}
	}

	ep := cfg.GetOAuthEndpointOverride("nonexistent")
	if ep.TokenURL != "" {
		t.Errorf("GetOAuthEndpointOverride(nonexistent) should return empty, got %q", ep.TokenURL)
	}
}

func TestGetOAuthEndpointOverride_ApiBaseURLOverride(t *testing.T) {
	cfg := &Config{
		OAuthEndpointOverrides: map[string]OAuthEndpointConfig{
			"kiro": {ApiBaseURL: "https://custom-oidc.example.com"},
		},
	}

	ep := cfg.GetOAuthEndpointOverride("kiro")
	if ep.ApiBaseURL != "https://custom-oidc.example.com" {
		t.Errorf("ApiBaseURL = %q, want %q", ep.ApiBaseURL, "https://custom-oidc.example.com")
	}
}

func TestGetOAuthEndpointOverride_DeviceAuthorizeURL(t *testing.T) {
	cfg := &Config{
		OAuthEndpointOverrides: map[string]OAuthEndpointConfig{
			"github-copilot": {DeviceAuthorizeURL: "https://custom-github.example.com/login/device/code"},
			"kimi":           {DeviceAuthorizeURL: "https://custom-kimi.example.com/device"},
		},
	}

	ep := cfg.GetOAuthEndpointOverride("github-copilot")
	if ep.DeviceAuthorizeURL != "https://custom-github.example.com/login/device/code" {
		t.Errorf("github-copilot DeviceAuthorizeURL = %q, want custom", ep.DeviceAuthorizeURL)
	}

	ep = cfg.GetOAuthEndpointOverride("kimi")
	if ep.DeviceAuthorizeURL != "https://custom-kimi.example.com/device" {
		t.Errorf("kimi DeviceAuthorizeURL = %q, want custom", ep.DeviceAuthorizeURL)
	}
}

func TestGetOAuthEndpointOverride_UserinfoURL(t *testing.T) {
	cfg := &Config{
		OAuthEndpointOverrides: map[string]OAuthEndpointConfig{
			"antigravity":    {UserinfoURL: "https://custom.googleapis.com/userinfo"},
			"github-copilot": {UserinfoURL: "https://custom-github.example.com/user"},
			"gemini":         {UserinfoURL: "https://custom.googleapis.com/oauth2/userinfo"},
			"kiro":           {UserinfoURL: "https://custom-oidc.example.com/userinfo"},
		},
	}

	for _, provider := range []string{"antigravity", "github-copilot", "gemini", "kiro"} {
		ep := cfg.GetOAuthEndpointOverride(provider)
		if ep.UserinfoURL == "" {
			t.Errorf("GetOAuthEndpointOverride(%q) returned empty UserinfoURL", provider)
		}
	}
}

func TestGetOAuthEndpointOverride_MultipleFieldsOverride(t *testing.T) {
	cfg := &Config{
		OAuthEndpointOverrides: map[string]OAuthEndpointConfig{
			"claude": {
				AuthorizeURL:       "https://custom.claude.ai/oauth/authorize",
				TokenURL:           "https://custom.api.anthropic.com/v1/oauth/token",
				RefreshURL:         "https://custom.api.anthropic.com/v1/oauth/refresh",
				UserinfoURL:        "https://custom.api.anthropic.com/v1/userinfo",
				DeviceAuthorizeURL: "https://custom.claude.ai/device",
			},
		},
	}

	ep := cfg.GetOAuthEndpointOverride("claude")
	if ep.AuthorizeURL == "" || ep.TokenURL == "" || ep.RefreshURL == "" ||
		ep.UserinfoURL == "" || ep.DeviceAuthorizeURL == "" {
		t.Errorf("Expected all fields populated, got: %+v", ep)
	}
}

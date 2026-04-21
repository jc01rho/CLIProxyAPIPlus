package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestClaudeAuthEndpointsDoNotDeriveOAuthURLsFromApiBaseURL(t *testing.T) {
	auth := &ClaudeAuth{
		cfg: &config.Config{
			OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{
				"claude": {ApiBaseURL: "https://proxy.example.com/v1"},
			},
		},
	}

	if got := auth.authEndpoint(); got != AuthURL {
		t.Fatalf("authEndpoint() = %q, want %q", got, AuthURL)
	}
	if got := auth.tokenEndpoint(false); got != TokenURL {
		t.Fatalf("tokenEndpoint(false) = %q, want %q", got, TokenURL)
	}
	if got := auth.tokenEndpoint(true); got != TokenURL {
		t.Fatalf("tokenEndpoint(true) = %q, want %q", got, TokenURL)
	}
}

func TestClaudeAuthExplicitOverrideBeatsApiBaseURL(t *testing.T) {
	auth := &ClaudeAuth{
		cfg: &config.Config{
			OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{
				"claude": {
					ApiBaseURL:   "https://proxy.example.com/v1",
					AuthorizeURL: "https://custom.example.com/oauth/authorize",
					TokenURL:     "https://custom.example.com/oauth/token",
					RefreshURL:   "https://custom.example.com/oauth/refresh",
				},
			},
		},
	}

	if got := auth.authEndpoint(); got != "https://custom.example.com/oauth/authorize" {
		t.Fatalf("authEndpoint() = %q, want explicit authorize URL", got)
	}
	if got := auth.tokenEndpoint(false); got != "https://custom.example.com/oauth/token" {
		t.Fatalf("tokenEndpoint(false) = %q, want explicit token URL", got)
	}
	if got := auth.tokenEndpoint(true); got != "https://custom.example.com/oauth/refresh" {
		t.Fatalf("tokenEndpoint(true) = %q, want explicit refresh URL", got)
	}
}

func TestClaudeCreateTokenStoragePersistsRuntimeBaseURL(t *testing.T) {
	auth := &ClaudeAuth{
		cfg: &config.Config{
			OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{
				"claude": {ApiBaseURL: "https://proxy.example.com/anthropic"},
			},
		},
	}

	storage := auth.CreateTokenStorage(&ClaudeAuthBundle{
		TokenData: ClaudeTokenData{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			Email:        "user@example.com",
			Expire:       "2099-01-01T00:00:00Z",
		},
		LastRefresh: "2099-01-01T00:00:00Z",
	})

	if storage.BaseURL != "https://proxy.example.com/anthropic" {
		t.Fatalf("storage.BaseURL = %q, want %q", storage.BaseURL, "https://proxy.example.com/anthropic")
	}
}

func TestClaudeAuthBlankExplicitOverrideFallsBackToDefaultEndpoints(t *testing.T) {
	auth := &ClaudeAuth{
		cfg: &config.Config{
			OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{
				"claude": {
					ApiBaseURL:   "https://proxy.example.com/v1",
					AuthorizeURL: "   ",
					TokenURL:     "",
					RefreshURL:   " ",
				},
			},
		},
	}

	if got := auth.authEndpoint(); got != AuthURL {
		t.Fatalf("authEndpoint() = %q, want %q", got, AuthURL)
	}
	if got := auth.tokenEndpoint(false); got != TokenURL {
		t.Fatalf("tokenEndpoint(false) = %q, want %q", got, TokenURL)
	}
	if got := auth.tokenEndpoint(true); got != TokenURL {
		t.Fatalf("tokenEndpoint(true) = %q, want %q", got, TokenURL)
	}
}

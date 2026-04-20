package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestClaudeAuthEndpointsUseApiBaseURLFallback(t *testing.T) {
	auth := &ClaudeAuth{
		cfg: &config.Config{
			OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{
				"claude": {ApiBaseURL: "https://proxy.example.com/v1"},
			},
		},
	}

	if got := auth.authEndpoint(); got != "https://proxy.example.com/oauth/authorize" {
		t.Fatalf("authEndpoint() = %q, want %q", got, "https://proxy.example.com/oauth/authorize")
	}
	if got := auth.tokenEndpoint(false); got != "https://proxy.example.com/oauth/token" {
		t.Fatalf("tokenEndpoint(false) = %q, want %q", got, "https://proxy.example.com/oauth/token")
	}
	if got := auth.tokenEndpoint(true); got != "https://proxy.example.com/oauth/token" {
		t.Fatalf("tokenEndpoint(true) = %q, want %q", got, "https://proxy.example.com/oauth/token")
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

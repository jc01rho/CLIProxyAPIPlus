package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare/lifecycle"
)

func oauthRegistrarFromEnv(proxy *lifecycle.HTTPProxy) (lifecycle.OAuthRegistrar, error) {
	path := strings.TrimSpace(os.Getenv("TEAM_MEMBER_OAUTH_CONFIG"))
	if path == "" {
		return nil, fmt.Errorf("TEAM_MEMBER_OAUTH_CONFIG is required for join --confirm")
	}
	cfg, err := lifecycle.LoadOAuthConfig(path)
	if err != nil {
		return nil, fmt.Errorf("oauth config: %w", err)
	}
	return &lifecycle.BrowserOAuth{Proxy: proxy, Config: cfg}, nil
}

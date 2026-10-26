package main

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare/lifecycle"
)

func oauthRegistrarFromEnv(_ *lifecycle.HTTPProxy) (lifecycle.OAuthRegistrar, error) {
	return nil, fmt.Errorf("oauth registrar is not wired yet")
}

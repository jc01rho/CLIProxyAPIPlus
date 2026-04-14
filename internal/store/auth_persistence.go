package store

import (
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func shouldPersistDisabledAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
		return false
	}
	if auth.PrimaryInfo == nil {
		return false
	}
	return !auth.PrimaryInfo.IsPrimary
}

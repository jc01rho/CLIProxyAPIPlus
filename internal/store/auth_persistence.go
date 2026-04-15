package store

import (
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func syncPrimaryInfoMetadata(auth *cliproxyauth.Auth) {
	cliproxyauth.SyncPrimaryInfoMetadata(auth)
	if auth == nil || auth.Metadata == nil {
		return
	}
	auth.Metadata["disabled"] = auth.Disabled
}

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

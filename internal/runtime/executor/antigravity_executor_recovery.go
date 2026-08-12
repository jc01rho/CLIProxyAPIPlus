package executor

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/antigravity"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// antigravityRecoveryErrorValue parses a response body into a value suitable for
// antigravity.IsRecoverableError. Returns nil for empty bodies and the raw string
// when the body is not valid JSON.
func antigravityRecoveryErrorValue(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return string(body)
	}
	return parsed
}

// antigravityAttemptSessionRecovery inspects the response body for recoverable
// Antigravity errors and, when found, invalidates the cached project context and
// strips any managed project ID from the auth so the next request re-resolves it.
func antigravityAttemptSessionRecovery(ctx context.Context, auth *cliproxyauth.Auth, body []byte) {
	errorValue := antigravityRecoveryErrorValue(body)
	if !antigravity.IsRecoverableError(errorValue) {
		return
	}
	if auth != nil {
		refresh := metaStringValue(auth.Metadata, "refresh_token")
		antigravity.InvalidateProjectContextCache(refresh)
		if auth.Metadata != nil {
			delete(auth.Metadata, "project_id")
			parts := antigravity.ParseRefreshParts(refresh)
			if parts.ManagedProjectID != "" {
				parts.ManagedProjectID = ""
				auth.Metadata["refresh_token"] = antigravity.FormatRefreshParts(parts)
			}
		}
	}
	if sessionID := strings.TrimSpace(gjson.GetBytes(body, "request.sessionId").String()); sessionID != "" {
		antigravity.RepairThinkingBlockOrder(sessionID, errorValue)
	}
	_ = ctx
}

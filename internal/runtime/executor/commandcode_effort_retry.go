package executor

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// commandCodePayloadModel returns the wire params.model value.
func commandCodePayloadModel(payload []byte) string {
	return gjson.GetBytes(payload, "params.model").String()
}

// commandCodePayloadEffort returns the wire params.reasoning_effort value.
func commandCodePayloadEffort(payload []byte) string {
	return gjson.GetBytes(payload, "params.reasoning_effort").String()
}

// commandCodeStripReasoningEffort returns a copy of the wire payload with
// params.reasoning_effort removed, or nil when the field is absent.
func commandCodeStripReasoningEffort(payload []byte) []byte {
	if !gjson.GetBytes(payload, "params.reasoning_effort").Exists() {
		return nil
	}
	updated, err := sjson.DeleteBytes(payload, "params.reasoning_effort")
	if err != nil {
		return nil
	}
	return updated
}

// commandCodeEffortRejectionRetryPayload returns a payload with reasoning_effort
// stripped when the given 400/422 response is a reasoning-effort rejection and
// the refreshed ladder no longer supports the requested effort. Returns nil
// when no retry is warranted (the caller should surface the original error).
//
// Mirrors opencodex's fetchResponse retry: on an effort rejection the model's
// profile page is fetched to refresh the ladder; if the refreshed ladder no
// longer documents the requested effort, the request is retried once without
// the field rather than failing the turn.
func commandCodeEffortRejectionRetryPayload(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	payload []byte,
	status int,
	body string,
) []byte {
	if !commandCodeIsReasoningEffortRejection(status, body) {
		return nil
	}
	modelID := commandCodePayloadModel(payload)
	currentEffort := commandCodePayloadEffort(payload)
	if modelID == "" || currentEffort == "" {
		return nil
	}
	refreshed := commandCodeRefreshReasoningEfforts(ctx, cfg, auth, modelID)
	if refreshed == nil || commandCodeEffortSupported(refreshed, currentEffort) {
		return nil
	}
	return commandCodeStripReasoningEffort(payload)
}

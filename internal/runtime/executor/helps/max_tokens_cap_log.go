package helps

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// LogMaxTokensCapped records that a request's max_tokens was lowered to the
// upstream limit, with the caller's masked API key so oversized senders can be found.
func LogMaxTokensCapped(ctx context.Context, provider, model string, requested, applied int64) {
	LogWithRequestID(ctx).WithFields(map[string]any{
		"provider":          provider,
		"model":             model,
		"requested_tokens":  requested,
		"applied_tokens":    applied,
		"downstream_apikey": util.HideAPIKey(APIKeyFromContext(ctx)),
	}).Warn("max_tokens exceeds the upstream limit; lowered")
}

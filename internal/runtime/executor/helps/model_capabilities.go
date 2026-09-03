package helps

import (
	"context"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// APIKeyModelIsCompat reports whether the selected API-key model enables
// compatibility handling for Claude thinking blocks.
func APIKeyModelIsCompat(req cliproxyexecutor.Request) bool {
	modelInfo, ok := cliproxyauth.ResolvedAPIKeyModelInfo(req)
	return ok && modelInfo != nil && modelInfo.IsCompat
}

// ApplyRequestThinking preserves the registry lookup path unless the auth
// manager bound authoritative model capabilities to this execution attempt.
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string) ([]byte, error) {
	return ApplyRequestThinkingWithContext(context.Background(), body, req, opts, fromFormat, toFormat, provider)
}

// ApplyRequestThinkingWithContext preserves request metadata for thinking
// validation warnings emitted during provider execution.
func ApplyRequestThinkingWithContext(ctx context.Context, body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string) ([]byte, error) {
	ctx = thinking.WithRequestLogMetadata(
		ctx,
		internallogging.GetRequestID(ctx),
		internallogging.GetClientRequestMetadata(ctx).APIKey,
	)
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	summaryConfig := translatedRequestSummaryConfig(body, req.Payload, originalSource, req.Model, fromFormat, toFormat)
	if modelInfo, ok := cliproxyauth.ResolvedModelInfo(req); ok {
		return thinking.ApplyThinkingWithModelInfoAndSummaryContext(ctx, body, originalSource, req.Model, fromFormat, toFormat, provider, modelInfo, summaryConfig)
	}
	return thinking.ApplyThinkingWithSummaryContext(ctx, body, req.Model, fromFormat, toFormat, provider, summaryConfig)
}

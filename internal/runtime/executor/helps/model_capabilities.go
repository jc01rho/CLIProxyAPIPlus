package helps

import (
	"context"
	"github.com/MoeForge/moego/ojson"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// APIKeyModelIsCompat reports whether the selected API-key model enables
// compatibility handling for Claude thinking blocks.
func APIKeyModelIsCompat(req cliproxyexecutor.Request) bool {
	modelInfo, ok := cliproxyauth.ResolvedModelInfo(req)
	return ok && modelInfo != nil && modelInfo.IsCompat
}

// ApplyRequestThinking preserves the registry lookup path unless the auth
// manager bound authoritative model capabilities to this execution attempt.
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, normalizedUpdatesChanged ...bool) ([]byte, error) {
	return ApplyRequestThinkingWithModelInfoContext(context.Background(), body, req, opts, fromFormat, toFormat, provider, nil, normalizedUpdatesChanged...)
}

// ApplyRequestThinkingWithContext preserves request metadata for thinking
// validation warnings emitted during provider execution.
func ApplyRequestThinkingWithContext(ctx context.Context, body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, normalizedUpdatesChanged ...bool) ([]byte, error) {
	return ApplyRequestThinkingWithModelInfoContext(ctx, body, req, opts, fromFormat, toFormat, provider, nil, normalizedUpdatesChanged...)
}

// ApplyRequestThinkingDoc applies request thinking onto doc in place, preserving
// the registry lookup path unless the auth manager bound authoritative model
// capabilities to this execution attempt.
func ApplyRequestThinkingDoc(doc *ojson.Doc, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, providerKey string, PreserveConfigurationUpdates ...bool) error {
	return ApplyRequestThinkingWithModelInfoDoc(doc, req, opts, fromFormat, toFormat, providerKey, nil, PreserveConfigurationUpdates...)
}

// ApplyRequestThinkingWithModelInfo applies request thinking and falls back to
// fallbackModelInfo when neither request metadata nor the global registry
// resolves a model definition.
func ApplyRequestThinkingWithModelInfo(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, fallbackModelInfo *registry.ModelInfo, normalizedUpdatesChanged ...bool) ([]byte, error) {
	return ApplyRequestThinkingWithModelInfoContext(context.Background(), body, req, opts, fromFormat, toFormat, provider, fallbackModelInfo, normalizedUpdatesChanged...)
}

// ApplyRequestThinkingWithModelInfoContext combines request-log metadata with
// the fallback model definition used by plugin executors.
func ApplyRequestThinkingWithModelInfoContext(ctx context.Context, body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, fallbackModelInfo *registry.ModelInfo, normalizedUpdatesChanged ...bool) ([]byte, error) {
	ctx = thinking.WithRequestLogMetadata(
		ctx,
		internallogging.GetRequestID(ctx),
		internallogging.GetClientRequestMetadata(ctx).APIKey,
	)
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	source := req.Payload
	if len(source) == 0 {
		source = opts.OriginalRequest
	}
	summaryConfig := translatedRequestSummaryConfig(body, req.Payload, originalSource, req.Model, fromFormat, toFormat)
	if modelInfo, ok := cliproxyauth.ResolvedModelInfo(req); ok {
		return thinking.ApplyThinkingWithModelInfoAndSummaryContext(ctx, body, source, req.Model, fromFormat, toFormat, provider, modelInfo, summaryConfig, normalizedUpdatesChanged...)
	}
	return thinking.ApplyThinkingWithFallbackModelInfoAndSummaryContext(ctx, body, source, req.Model, fromFormat, toFormat, provider, fallbackModelInfo, summaryConfig, normalizedUpdatesChanged...)
}

// ApplyRequestThinkingWithContextDoc applies request thinking onto doc in place and
// preserves request metadata for thinking validation warnings.
func ApplyRequestThinkingWithContextDoc(ctx context.Context, doc *ojson.Doc, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, normalizedUpdatesChanged ...bool) error {
	return ApplyRequestThinkingWithModelInfoContextDoc(ctx, doc, req, opts, fromFormat, toFormat, provider, nil, normalizedUpdatesChanged...)
}

// ApplyRequestThinkingWithModelInfoDoc applies request thinking onto doc in place
// and falls back to fallbackModelInfo when neither request metadata nor the global
// registry resolves a model definition.
func ApplyRequestThinkingWithModelInfoDoc(doc *ojson.Doc, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, fallbackModelInfo *registry.ModelInfo, normalizedUpdatesChanged ...bool) error {
	return ApplyRequestThinkingWithModelInfoContextDoc(context.Background(), doc, req, opts, fromFormat, toFormat, provider, fallbackModelInfo, normalizedUpdatesChanged...)
}

// ApplyRequestThinkingWithModelInfoContextDoc combines request-log metadata with
// the fallback model definition and applies request thinking onto doc in place.
func ApplyRequestThinkingWithModelInfoContextDoc(ctx context.Context, doc *ojson.Doc, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, fallbackModelInfo *registry.ModelInfo, normalizedUpdatesChanged ...bool) error {
	if doc == nil {
		return nil
	}
	ctx = thinking.WithRequestLogMetadata(
		ctx,
		internallogging.GetRequestID(ctx),
		internallogging.GetClientRequestMetadata(ctx).APIKey,
	)
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	source := req.Payload
	if len(source) == 0 {
		source = opts.OriginalRequest
	}
	summaryConfig := translatedRequestSummaryConfigDoc(doc, req.Payload, originalSource, req.Model, fromFormat, toFormat)
	if modelInfo, ok := cliproxyauth.ResolvedModelInfo(req); ok {
		return thinking.ApplyThinkingWithModelInfoAndSummaryContextDoc(ctx, doc, source, req.Model, fromFormat, toFormat, provider, modelInfo, summaryConfig, normalizedUpdatesChanged...)
	}
	return thinking.ApplyThinkingWithFallbackModelInfoAndSummaryContextDoc(ctx, doc, source, req.Model, fromFormat, toFormat, provider, fallbackModelInfo, summaryConfig, normalizedUpdatesChanged...)
}

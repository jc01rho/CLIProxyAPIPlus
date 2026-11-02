package executor

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// openCodeAffinitySession derives the OpenCode session id that pins a
// conversation to one upstream backend, from the proxy's reconciled session
// identity, hashed into the CLI's opaque descending ses_ shape so the raw
// downstream id never reaches the provider. The LCP affinity id is
// routing-only, so a canonical id that only mirrors it is skipped. Empty means
// no conversation identity is available.
func openCodeAffinitySession(ctx context.Context, metadataSets ...map[string]any) string {
	if identity := openCodeSessionIdentity(metadataSets...); identity != "" {
		return opencodeHashSession("opencode-session:" + identity)
	}
	if identity := util.SessionIDFromContext(ctx); identity != "" {
		return opencodeHashSession("opencode-session:" + identity)
	}
	return ""
}

// openCodeSessionIdentity returns the first metadata identity that is not the
// LCP routing id.
func openCodeSessionIdentity(metadataSets ...map[string]any) string {
	for _, metadata := range metadataSets {
		if metadata == nil {
			continue
		}
		lcpID := metadataString(metadata, cliproxyexecutor.LCPAffinitySessionIDMetadataKey)
		for _, key := range []string{
			cliproxyexecutor.CanonicalSessionIDMetadataKey,
			cliproxyexecutor.DerivedSessionIDMetadataKey,
			cliproxyexecutor.ExecutionSessionMetadataKey,
		} {
			if identity := metadataString(metadata, key); identity != "" && identity != lcpID {
				return identity
			}
		}
	}
	return ""
}

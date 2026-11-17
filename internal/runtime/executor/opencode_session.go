package executor

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// openCodeAffinitySession derives the OpenCode session id that pins a
// conversation to one upstream backend, from the proxy's reconciled session
// identity, hashed into the CLI's opaque descending ses_ shape so the raw
// downstream id never reaches the provider. The LCP affinity id is
// routing-only, so any identity that is the LCP routing id, from metadata or
// from the context session the conductor mirrors it into, is skipped. Empty
// means no conversation identity is available.
func openCodeAffinitySession(ctx context.Context, metadataSets ...map[string]any) string {
	if identity := openCodeSessionIdentity(metadataSets...); identity != "" {
		return opencodeHashSession("opencode-session:" + identity)
	}
	if identity := util.SessionIDFromContext(ctx); identity != "" && !isLCPRoutingIdentity(identity, metadataSets...) {
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
		for _, key := range []string{
			cliproxyexecutor.CanonicalSessionIDMetadataKey,
			cliproxyexecutor.DerivedSessionIDMetadataKey,
			cliproxyexecutor.ExecutionSessionMetadataKey,
		} {
			if identity := metadataString(metadata, key); identity != "" && !isLCPRoutingIdentity(identity, metadataSets...) {
				return identity
			}
		}
	}
	return ""
}

// isLCPRoutingIdentity reports whether identity is an LCP routing id: either
// equal to an LCP affinity id carried by any metadata set, or carrying the
// "lcp:" prefix that LCP ids use (see session.CandidateSessionPrefixes).
func isLCPRoutingIdentity(identity string, metadataSets ...map[string]any) bool {
	if strings.HasPrefix(identity, "lcp:") {
		return true
	}
	for _, metadata := range metadataSets {
		if metadata == nil {
			continue
		}
		if lcpID := metadataString(metadata, cliproxyexecutor.LCPAffinitySessionIDMetadataKey); lcpID != "" && lcpID == identity {
			return true
		}
	}
	return false
}

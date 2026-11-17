package auth

import (
	"sync"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const cloudflareBlockedAuthsMetadataKey = "cliproxy.cloudflare_blocked_auths"

// cloudflareBlockedAuths records credentials that received a Cloudflare/WAF
// block page during one client request. The block is not a credential or quota
// problem, so it never cools the credential when cooling is disabled; this set
// keeps the credential out of every later retry round of the same request so
// the request falls back to another credential or provider instead of hitting
// the same block again.
type cloudflareBlockedAuths struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (b *cloudflareBlockedAuths) add(authID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ids == nil {
		b.ids = make(map[string]struct{})
	}
	b.ids[authID] = struct{}{}
}

func (b *cloudflareBlockedAuths) snapshot() map[string]struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]struct{}, len(b.ids))
	for id := range b.ids {
		out[id] = struct{}{}
	}
	return out
}

func cloudflareBlockedAuthsFromMetadata(meta map[string]any) *cloudflareBlockedAuths {
	tracker, _ := meta[cloudflareBlockedAuthsMetadataKey].(*cloudflareBlockedAuths)
	return tracker
}

func withCloudflareBlockedAuthTracker(opts cliproxyexecutor.Options) cliproxyexecutor.Options {
	if cloudflareBlockedAuthsFromMetadata(opts.Metadata) != nil {
		return opts
	}
	meta := cloneRequestMetadata(opts.Metadata)
	meta[cloudflareBlockedAuthsMetadataKey] = &cloudflareBlockedAuths{}
	opts.Metadata = meta
	return opts
}

func cloudflareBlockedAuthIDs(opts cliproxyexecutor.Options) map[string]struct{} {
	tracker := cloudflareBlockedAuthsFromMetadata(opts.Metadata)
	if tracker == nil {
		return nil
	}
	return tracker.snapshot()
}

func recordCloudflareBlockedAuth(result Result) {
	if result.Success || !isCloudflareChallengeResultError(result.Error) {
		return
	}
	if tracker := cloudflareBlockedAuthsFromMetadata(result.Options.Metadata); tracker != nil {
		tracker.add(result.AuthID)
	}
}

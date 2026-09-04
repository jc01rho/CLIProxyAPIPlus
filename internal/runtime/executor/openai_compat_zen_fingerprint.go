package executor

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
)

// The OpenCode Zen gateway fingerprints non-Zen clients on
// https://opencode.ai/zen/v1: the official opencode app tags every request
// with x-opencode-* identity headers and an opencode/<version> User-Agent
// (sst/opencode packages/opencode/src/session/llm/request.ts). Providers
// proxied through Zen (e.g. openai-compatible-opencode-free) reject requests
// that lack the fingerprint, so mirror the app's wire identity here.
const (
	opencodeZenHostSuffix        = "opencode.ai"
	opencodeFingerprintUserAgent = "opencode/1.18.23"
	opencodeFingerprintClient    = "cli"
)

// looksLikeOpencodeZenBaseURL reports whether the upstream endpoint points at
// the OpenCode Zen gateway.
func looksLikeOpencodeZenBaseURL(baseURL string) bool {
	u := strings.TrimSpace(baseURL)
	if u == "" {
		return false
	}
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	host := u
	if idx := strings.IndexAny(u, "/?#"); idx >= 0 {
		host = u[:idx]
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == opencodeZenHostSuffix || strings.HasSuffix(host, "."+opencodeZenHostSuffix)
}

// providerKeyLooksOpencode reports whether the provider key is an opencode
// branded openai-compatible entry (e.g. "openai-compatible-opencode-free").
func providerKeyLooksOpencode(providerKey string) bool {
	key := strings.ToLower(strings.TrimSpace(providerKey))
	if key == "" {
		return false
	}
	key = strings.TrimPrefix(key, "openai-compatible-")
	return key == "opencode" || strings.HasPrefix(key, "opencode-") || strings.HasPrefix(key, "opencode_")
}

// shouldApplyOpencodeZenFingerprint gates the fingerprint on either signal:
// a Zen base URL or an opencode-branded provider key.
func shouldApplyOpencodeZenFingerprint(baseURL, providerKey string) bool {
	return looksLikeOpencodeZenBaseURL(baseURL) || providerKeyLooksOpencode(providerKey)
}

// newOpencodeRequestID returns a UUID v4 string for x-opencode-request /
// x-opencode-session, mirroring the randomUUID() the opencode app sends.
func newOpencodeRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0197ffff-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// applyOpencodeZenFingerprint rewrites the request identity so the Zen gateway
// sees the same x-opencode-* header set and User-Agent the official opencode
// app sends. Values are per-request (UUID v4 session/request ids, cli client
// flag), matching request.ts:186-205 in sst/opencode.
func applyOpencodeZenFingerprint(req *http.Request, providerKey string) {
	if req == nil {
		return
	}
	baseURL := ""
	if req.URL != nil {
		baseURL = req.URL.String()
	}
	if !shouldApplyOpencodeZenFingerprint(baseURL, providerKey) {
		return
	}
	req.Header.Set("x-opencode-session", newOpencodeRequestID())
	req.Header.Set("x-opencode-request", newOpencodeRequestID())
	req.Header.Set("x-opencode-client", opencodeFingerprintClient)
	req.Header.Set("User-Agent", opencodeFingerprintUserAgent)
}

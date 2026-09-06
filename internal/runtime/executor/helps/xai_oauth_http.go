package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ApplyXAIOAuthHTTPHeaders implements the opencodex v2.43.0 xai-transport.ts
// compatibility contract. The caller applies configured header overrides last.
func ApplyXAIOAuthHTTPHeaders(r *http.Request, promptCacheKey, credentialTarget string, stream bool) {
	r.Header.Set("User-Agent", "opencodex-grok/0.2.93")
	r.Header.Set("x-grok-client-identifier", "opencodex")
	r.Header.Set("x-grok-client-version", "0.2.93")
	r.Header.Set("x-xai-token-auth", "xai-grok-cli")
	r.Header.Set("x-authenticateresponse", "authenticate-response")
	if key := strings.TrimSpace(promptCacheKey); key != "" {
		sum := sha256.Sum256([]byte(key))
		affinity := hex.EncodeToString(sum[:16])
		r.Header.Set("x-grok-conv-id", affinity)
		r.Header.Set("x-grok-session-id", affinity)
	}
	r.Header.Set("x-grok-req-id", cliproxyexecutor.XAIRequestID(r.Context(), credentialTarget))
	if stream {
		r.Header.Set("Accept-Encoding", "identity")
	}
}

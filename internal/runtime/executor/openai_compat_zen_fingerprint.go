package executor

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
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
// app sends, matching request.ts:186-205 in sst/opencode.
//
// x-opencode-session carries a conversation-stable fingerprint (PR 12719 /
// 12660 pattern): the official app sends one session id per conversation so
// upstream keeps the prompt cache warm and, from 2026-09-06, errors on
// requests with no session header at all. When the caller supplies an
// existing session header (forwarded from the client), it wins untouched;
// otherwise the session id derives from the request body's model + system
// prompt + first user message + tools, so consecutive agent turns of one
// conversation share a session. x-opencode-request stays per-request UUID.
func applyOpencodeZenFingerprint(req *http.Request, providerKey string, body []byte) {
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
	if req.Header.Get("x-opencode-session") == "" {
		req.Header.Set("x-opencode-session", opencodeSessionFingerprint(body))
	}
	req.Header.Set("x-opencode-request", newOpencodeRequestID())
	req.Header.Set("x-opencode-client", opencodeFingerprintClient)
	req.Header.Set("User-Agent", opencodeFingerprintUserAgent)
}

// opencodeSessionFingerprint derives a conversation-stable x-opencode-session
// value from the request body: model, system prompt, first user message and
// tool names hash into a UUID-shaped id (UUID formatting keeps the wire
// shape; the entropy source is the conversation prefix, not randomness).
// Falls back to a random UUID when the body yields no stable fields, mirroring
// OmniRoute PR 12719's "random UUID when no body is available" branch.
func opencodeSessionFingerprint(body []byte) string {
	fields := make([]string, 0, 4)
	if v := gjson.GetBytes(body, "model").String(); v != "" {
		fields = append(fields, "model:"+v)
	}
	if v := gjson.GetBytes(body, "system").String(); v != "" {
		fields = append(fields, "system:"+v)
	}
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			if content := msg.Get("content"); content.Exists() {
				fields = append(fields, "user:"+content.String())
			}
			return false // first user message only
		}
		return true
	})
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		if name := tool.Get("function.name").String(); name != "" {
			fields = append(fields, "tool:"+name)
		} else if name := tool.Get("name").String(); name != "" {
			fields = append(fields, "tool:"+name)
		}
		return true
	})
	if len(fields) == 0 {
		return newOpencodeRequestID()
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x1f")))
	b := [16]byte{}
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x40 // keep UUID v4 wire shape
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

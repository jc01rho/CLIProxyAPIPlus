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
// x-opencode-session is PROVIDER-OWNED output (decolua/9router PR 3780): the
// inbound x-opencode-session header from the client is never forwarded, so a
// caller cannot influence or spoof upstream session affinity. Derivation
// priority, all stable within a conversation:
//  1. the client's proxy-session identity (X-Session-Id / X-Session-Affinity),
//     hashed opaque — explicit conversation identity beats body heuristics;
//  2. the conversation-prefix fingerprint of the request body (model +
//     system + first user message + tools) — upstream keeps its prompt cache
//     warm and, from 2026-09-06, errors when no session header is present;
//  3. a stable per-credential hash of the API key (e.g. image generations,
//     whose multipart bodies yield no body fields);
//  4. a random UUID when none of the above are available.
//
// x-opencode-request stays per-request UUID.
func applyOpencodeZenFingerprint(req *http.Request, providerKey string, body []byte, clientHeaders http.Header, fallbackSeed string) {
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
	req.Header.Set("x-opencode-session", opencodeSessionValue(clientHeaders, body, fallbackSeed))
	req.Header.Set("x-opencode-request", newOpencodeRequestID())
	req.Header.Set("x-opencode-client", opencodeFingerprintClient)
	req.Header.Set("User-Agent", opencodeFingerprintUserAgent)
}

// opencodeSessionValue resolves the provider-owned x-opencode-session per the
// priority chain above; every branch produces an opaque UUID-shaped id that
// reveals nothing about its input.
func opencodeSessionValue(clientHeaders http.Header, body []byte, fallbackSeed string) string {
	if clientHeaders != nil {
		for _, name := range []string{"X-Session-Id", "X-Session-Affinity"} {
			if v := strings.TrimSpace(clientHeaders.Get(name)); v != "" {
				return opencodeHashSession("client-session:" + v)
			}
		}
	}
	if s := opencodeSessionFingerprint(body); s != "" {
		return s
	}
	if fallbackSeed != "" {
		return opencodeHashSession("credential:" + fallbackSeed)
	}
	return newOpencodeRequestID()
}

// opencodeHashSession renders an opaque UUID-v4-shaped id from arbitrary
// input (122-bit effective entropy after the RFC4122 bits are clobbered).
func opencodeHashSession(input string) string {
	sum := sha256.Sum256([]byte(input))
	b := [16]byte{}
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
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
	// system may arrive as a top-level string or as a role:"system" message
	// after translation; both shapes must hash the same conversation.
	if v := gjson.GetBytes(body, "system"); v.Exists() {
		if s := opencodeContentText(v); s != "" {
			fields = append(fields, "system:"+s)
		}
	} else {
		gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
			if msg.Get("role").String() == "system" {
				if s := opencodeContentText(msg.Get("content")); s != "" {
					fields = append(fields, "system:"+s)
				}
				return false
			}
			return true
		})
	}
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			if s := opencodeContentText(msg.Get("content")); s != "" {
				fields = append(fields, "user:"+s)
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
		return "" // caller falls through to the next stable branch
	}
	return opencodeHashSession(strings.Join(fields, "\x1f"))
}

// opencodeContentText normalizes OpenAI content shapes to plain text: a
// string passes through; an array of typed blocks ({type:text,text} or
// {type:...,content}) is concatenated so the same conversation whose block
// layout shifts between turns still hashes identically.
func opencodeContentText(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if !content.IsArray() {
		return content.String()
	}
	var text strings.Builder
	content.ForEach(func(_, block gjson.Result) bool {
		if s := block.Get("text").String(); s != "" {
			text.WriteString(s)
		} else if s := block.Get("content").String(); s != "" {
			text.WriteString(s)
		}
		return true
	})
	if text.Len() == 0 {
		return content.String()
	}
	return text.String()
}

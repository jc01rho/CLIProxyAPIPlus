package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ZCodeAnthropicBaseURL is the pinned Anthropic-compatible base for the zcode
// provider. glm-zcode logs in via ZCode's OAuth but auto-provisions a real
// Z.AI API key and calls api.z.ai directly (no zcode.z.ai gateway, no
// captcha). Pin the base so dynamic discovery / stale catalogs can't redirect
// it elsewhere.
const ZCodeAnthropicBaseURL = "https://api.z.ai/api/anthropic"

// ZCodeStartPlanBaseURL is the ZCode start-plan gateway, verified from the
// ZCode desktop app (app.asar buildZCodeEndpointUrls): the start/coding-plan
// billing routes live on the zcode.z.ai origin under /api/v1/zcode-plan. When
// the account has an active start plan, requests are routed through this
// gateway with the broker JWT so the start plan quota is consumed instead of
// the individual plan that the provisioned Z.AI API key bills to. The
// zcode-plan model paths are exempt from the desktop client's Ed25519 signing
// (isUnsignedModelRequestPath), so a plain Bearer JWT works.
const ZCodeStartPlanBaseURL = "https://zcode.z.ai/api/v1/zcode-plan/anthropic"

// ZCodeAppVersion mirrors the ZCode desktop release used for source headers
// and the balance probe. Keep it aligned with a real published release so the
// gateway treats the client as a current ZCode build (verified against the
// 3.10.2 linux-x64 AppImage build metadata).
const zcodeAppVersion = "3.10.2"

// zcodeReleaseChannel is the release channel used in the ZCode source headers.
const zcodeReleaseChannel = "stable"

// ZcodeExecutor is a stateless executor for the GLM ZCode provider. It reuses
// the Anthropic-compatible ClaudeExecutor request/stream path but pins the
// base URL to api.z.ai and injects the ZCode source headers so api.z.ai sees
// the request as the ZCode client.
type ZcodeExecutor struct {
	*ClaudeExecutor
}

// NewZcodeExecutor creates a zcode executor.
func NewZcodeExecutor(cfg *config.Config) *ZcodeExecutor {
	return &ZcodeExecutor{ClaudeExecutor: NewClaudeExecutor(cfg)}
}

// Identifier returns the executor identifier.
func (e *ZcodeExecutor) Identifier() string {
	return "zcode"
}

// Execute runs a non-streaming zcode request.
func (e *ZcodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	auth, opts = e.prepareZcodeRequest(auth, opts)
	return e.ClaudeExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream runs a streaming zcode request.
func (e *ZcodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	auth, opts = e.prepareZcodeRequest(auth, opts)
	return e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
}

// prepareZcodeRequest pins the base URL, injects the ZCode source headers, and
// attaches the desktop client's session identity header.
//
// Routing: when the auth record carries a broker zcode JWT and an active start
// plan (probed at OAuth/refresh time via billing/balance), requests go through
// the zcode.z.ai start-plan gateway with the broker JWT so start plan quota is
// consumed instead of the individual plan that the provisioned Z.AI API key is
// otherwise billed to. Otherwise every request authenticates with the
// provisioned key against api.z.ai, matching the gajae-code reference
// implementation (packages/ai/src/utils/oauth/glm-zcode.ts).
//
// History note: an earlier attempt at this routing failed with the gateway's
// {"code":3007,"msg":"captcha verify failed"}. The reverse-engineered Client
// Signing V4 shows the zcode-plan model paths are EXEMPT from Ed25519 signing
// (isUnsignedModelRequestPath), so the captcha is enforced by a separate
// Aliyun gate. The routing is restored balance-gated: only accounts whose
// billing/balance explicitly reports an active start plan take the gateway
// path, and any upstream rejection still surfaces to the caller.
//
// Wire delivery: ClaudeExecutor never reads opts.Headers for the outbound
// request, so injecting here would silently vanish. Headers travel in
// auth.Attributes as "header:<name>" entries — util.ApplyCustomHeadersFromAttrs
// replays them with Set() on the finished request, after the credential
// rewrite, exactly the seam the desktop's own provider entries use.
func (e *ZcodeExecutor) prepareZcodeRequest(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) (*cliproxyauth.Auth, cliproxyexecutor.Options) {
	if auth != nil {
		auth = auth.Clone()
		if auth.Attributes == nil {
			auth.Attributes = map[string]string{}
		}
		useGateway := zcodeBrokerToken(auth) != "" && zcodeUseStartPlan(auth)
		if useGateway {
			auth.Attributes["base_url"] = ZCodeStartPlanBaseURL
			// The broker JWT is the credential, not an extra header. Verified from
			// app.asar loadPresetProviders: the zaiStartPlan provider entry is built
			// as {id: zaiStartPlan, endpoints:{baseURL: zcodePlanAnthropicBaseUrl},
			// apiKey: p} where p = loadZaiProviderConnectionZcodeJwtToken().
			//
			// It must occupy the credential slot rather than opts.Headers, because
			// ClaudeExecutor resolves claudeCreds(auth) and then unconditionally
			// rewrites Authorization/x-api-key from it in
			// applyClaudeHeadersWithNativeProfile. A JWT injected only as a header is
			// overwritten by the provisioned Z.AI key, which the gateway rejects with
			// 401. Metadata is overridden too: claudeCreds falls back to
			// Metadata["access_token"] whenever Attributes carries no api_key.
			auth.Attributes["api_key"] = zcodeBrokerToken(auth)
			if auth.Metadata != nil {
				metadata := make(map[string]any, len(auth.Metadata))
				for k, v := range auth.Metadata {
					metadata[k] = v
				}
				metadata["access_token"] = zcodeBrokerToken(auth)
				auth.Metadata = metadata
			}
		} else {
			auth.Attributes["base_url"] = ZCodeAnthropicBaseURL
		}
		zcodeSetWireHeaders(auth, useGateway)
	}
	return auth, opts
}

// zcodeSetWireHeaders materializes the ZCode desktop wire identity as
// "header:<name>" attributes. ClaudeExecutor's credential rewrite would drop
// anything injected into opts.Headers, but ApplyCustomHeadersFromAttrs replays
// these onto the finished upstream request after the auth rewrite. Called on
// the cloned auth only — the original record is never mutated.
func zcodeSetWireHeaders(auth *cliproxyauth.Auth, useGateway bool) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = map[string]string{}
	}
	for name, value := range buildZCodeSourceHeaders() {
		auth.Attributes["header:"+name] = value[0]
	}
	auth.Attributes["header:X-Session-Id"] = zcodeSessionID(auth)
}

// zcodeUseStartPlan reports whether the auth record has an active start plan
// and a broker token to route through the ZCode gateway with.
func zcodeUseStartPlan(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if v, ok := auth.Attributes["start_plan_active"]; ok && v == "true" {
		return zcodeBrokerToken(auth) != ""
	}
	if v, ok := auth.Metadata["start_plan_active"].(bool); ok && v {
		return zcodeBrokerToken(auth) != ""
	}
	// Legacy key persisted by pre-revert builds (v7.2.146-4 era).
	if v, ok := auth.Metadata["start_plan"].(bool); ok && v {
		return zcodeBrokerToken(auth) != ""
	}
	return false
}

// zcodeBrokerToken returns the broker JWT stored alongside the auth record.
// Attributes["zcode_token"] is the freshest copy (set in-memory right after
// OAuth); Metadata["zcode_token"] survives reload from disk.
func zcodeBrokerToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if tok := strings.TrimSpace(auth.Attributes["zcode_token"]); tok != "" {
			return tok
		}
	}
	if auth.Metadata != nil {
		if tok, ok := auth.Metadata["zcode_token"].(string); ok {
			return strings.TrimSpace(tok)
		}
	}
	return ""
}

// buildZCodeSourceHeaders replicates ZCode's buildZCodeSourceHeaders() so
// api.z.ai sees the request as the ZCode client. Printable-ASCII only;
// platform/arch resolved at runtime.
func buildZCodeSourceHeaders() http.Header {
	h := http.Header{}
	h.Set("User-Agent", "ZCode/"+zcodeAppVersion)
	h.Set("HTTP-Referer", "https://zcode.z.ai")
	h.Set("X-Title", "Z Code@electron")
	h.Set("X-Platform", runtime.GOOS+"-"+runtime.GOARCH)
	h.Set("X-Client-Language", language())
	h.Set("X-Client-Timezone", timezone())
	h.Set("X-Os-Category", normalizeOsCategory(runtime.GOOS))
	h.Set("X-ZCode-Agent", "glm")
	h.Set("X-ZCode-App-Version", zcodeAppVersion)
	h.Set("X-Release-Channel", zcodeReleaseChannel)
	h.Set("X-Device-Mid", deviceMid())
	h.Set("X-Os-Version", osVersion())
	return h
}

// language returns the client language locale, falling back to "en-US".
func language() string {
	// Use golang.org/x/text/language for locale detection
	// For now, try environment variables first
	if locale := os.Getenv("LANG"); locale != "" {
		return strings.Split(locale, ".")[0]
	}
	if locale := os.Getenv("LC_ALL"); locale != "" {
		return strings.Split(locale, ".")[0]
	}
	if locale := os.Getenv("LANGUAGE"); locale != "" {
		return strings.Split(locale, ":")[0]
	}
	return "en-US"
}

// timezone returns the client timezone, falling back to "UTC".
func timezone() string {
	loc, _ := time.LoadLocation(os.Getenv("TZ"))
	if loc == nil {
		return "UTC"
	}
	return fmt.Sprintf("%s", loc)
}

// deviceMid reads the ZCode device ID from the telemetry file.
func deviceMid() string {
	const file = "telemetry-state.json"
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "ZCode", file)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	v, _ := m["deviceMid"].(string)
	return strings.TrimSpace(v)
}

// osVersion returns the OS version string.
func osVersion() string {
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "VERSION_ID=") {
					return strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), "\"")
				}
			}
		}
	}
	if runtime.GOOS == "darwin" {
		out, _ := exec.Command("sw_vers", "-productVersion").Output()
		return strings.TrimSpace(string(out))
	}
	if runtime.GOOS == "windows" {
		out, _ := exec.Command("cmd", "/c", "ver").Output()
		return strings.TrimSpace(string(out))
	}
	return ""
}

// normalizeOsCategory maps a GOOS to ZCode's OS category.
func normalizeOsCategory(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return "linux"
	}
}

// mergeHeaders merges extra headers into base, with extra winning.
func mergeHeaders(base, extra http.Header) http.Header {
	if base == nil {
		base = http.Header{}
	}
	for k, vs := range extra {
		for _, v := range vs {
			base.Set(k, v)
		}
	}
	return base
}

// zcodeCreds returns the provisioned Z.AI API key from the auth record.
//
// Attributes["api_key"] is populated in-memory right after OAuth, but zcode auth
// files persist as the flat TokenStorage form and carry no "attributes" object,
// so a record reloaded from disk has a nil Attributes map. Metadata["access_token"]
// holds the same provisioned Z.AI API key "{id}.{secret}" (see
// internal/auth/zcode/zcode.go Credentials.AccessToken), so it is the correct
// fallback: without it the key is silently lost on every restart or config reload.
func zcodeCreds(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
			return key
		}
	}
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["access_token"].(string); ok {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

// zcodeSessionID derives a stable desktop-style session identity for the auth
// record. The X-Session-Id value feeds the Client Signing V4 PoW salt and the
// gateway's session tracking, so it must stay stable across restarts for the
// same account. Hashing the account email/key avoids leaking any credential
// material while keeping per-account separation.
func zcodeSessionID(auth *cliproxyauth.Auth) string {
	seed := ""
	if auth != nil {
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["account_id"].(string); ok && v != "" {
				seed = v
			}
		}
		if seed == "" && auth.Attributes != nil {
			seed = auth.Attributes["email"]
		}
	}
	if seed == "" {
		seed = "zcode"
	}
	sum := sha256.Sum256([]byte("zcode-session\n" + seed))
	return hex.EncodeToString(sum[:16])
}

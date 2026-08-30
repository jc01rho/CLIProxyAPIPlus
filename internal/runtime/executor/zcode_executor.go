package executor

import (
	"context"
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
// zcode-plan paths are exempt from the app's Ed25519 client signing
// (isUnsignedModelRequestPath), so a plain Bearer JWT works.
const ZCodeStartPlanBaseURL = "https://zcode.z.ai/api/v1/zcode-plan/anthropic"

// zcodeStartPlanBalanceURL is the billing/balance endpoint used by the ZCode
// app to detect an active start plan (data.plans[] with status "active" and
// a plan_id/name containing "start-plan").
const zcodeStartPlanBalanceURL = "https://zcode.z.ai/api/v1/zcode-plan/billing/balance"

// ZCodeAppVersion mirrors the ZCode desktop release used for source headers
// and the balance probe. Keep it aligned with a real published release so the
// gateway treats the client as a current ZCode build.
const zcodeAppVersion = "3.10.1"

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

// prepareZcodeRequest pins the base URL and injects the ZCode source headers.
// When the account has an active start plan, the request is routed through the
// zcode.z.ai gateway with the broker JWT so start plan quota is consumed
// instead of the individual plan that the provisioned Z.AI API key is
// otherwise billed to.
func (e *ZcodeExecutor) prepareZcodeRequest(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) (*cliproxyauth.Auth, cliproxyexecutor.Options) {
	if auth != nil {
		auth = auth.Clone()
		if auth.Attributes == nil {
			auth.Attributes = map[string]string{}
		}
		if zcodeUseStartPlan(auth) {
			auth.Attributes["base_url"] = ZCodeStartPlanBaseURL
			if tok := zcodeBrokerToken(auth); tok != "" {
				// Verified from app.asar buildAnthropicConnectivityAuthHeaders: the
				// start-plan gateway authenticates the broker JWT via both
				// Authorization and x-api-key headers (Anthropic-format requests).
				opts.Headers = mergeHeaders(opts.Headers, http.Header{
					"Authorization": []string{"Bearer " + tok},
					"x-api-key":    []string{tok},
				})
			}
		} else {
			auth.Attributes["base_url"] = ZCodeAnthropicBaseURL
		}
	}
	opts.Headers = mergeHeaders(opts.Headers, buildZCodeSourceHeaders())
	return auth, opts
}

// zcodeUseStartPlan reports whether the auth record has an active start plan
// and a broker token to route through the ZCode gateway with. Falls back to
// re-checking the quota endpoint lazily when neither signal is cached yet.
func zcodeUseStartPlan(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if v, ok := auth.Attributes["start_plan_active"]; ok && v == "true" {
		return zcodeBrokerToken(auth) != ""
	}
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

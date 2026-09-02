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

// prepareZcodeRequest pins the base URL and injects the ZCode source headers.
//
// Every request authenticates with the provisioned Z.AI API key against
// api.z.ai, matching the gajae-code reference implementation
// (packages/ai/src/utils/oauth/glm-zcode.ts).
//
// Routing inference through the zcode.z.ai start-plan gateway was tried and
// removed: that origin answers model requests with {"code":3007,"msg":"captcha
// verify failed"} because it demands the desktop app's client attestation,
// which this proxy cannot produce. Consuming start-plan quota therefore is not
// reachable from here, and requests bill to the individual plan the
// provisioned key belongs to.
func (e *ZcodeExecutor) prepareZcodeRequest(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) (*cliproxyauth.Auth, cliproxyexecutor.Options) {
	if auth != nil {
		auth = auth.Clone()
		if auth.Attributes == nil {
			auth.Attributes = map[string]string{}
		}
		auth.Attributes["base_url"] = ZCodeAnthropicBaseURL
	}
	opts.Headers = mergeHeaders(opts.Headers, buildZCodeSourceHeaders())
	return auth, opts
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

package executor

import (
	"context"
	"net/http"
	"runtime"
	"strings"

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

// ZCodeAppVersion / release channel used in the ZCode source headers.
const (
	zcodeAppVersion     = "1.0.0"
	zcodeReleaseChannel = "stable"
)

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
	h.Set("X-Client-Language", "unknown")
	h.Set("X-Client-Timezone", "unknown")
	h.Set("X-Os-Category", normalizeOsCategory(runtime.GOOS))
	h.Set("X-ZCode-Agent", "glm")
	h.Set("X-ZCode-App-Version", zcodeAppVersion)
	h.Set("X-Release-Channel", zcodeReleaseChannel)
	return h
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

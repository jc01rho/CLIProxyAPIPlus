package executor

import (
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestBuildZCodeSourceHeaders verifies the ZCode source headers match the
// gajae-code buildZCodeSourceHeaders contract.
func TestBuildZCodeSourceHeaders(t *testing.T) {
	h := buildZCodeSourceHeaders()
	checks := map[string]string{
		"User-Agent":          "ZCode/3.10.2",
		"HTTP-Referer":        "https://zcode.z.ai",
		"X-Title":             "Z Code@electron",
		"X-ZCode-Agent":       "glm",
		"X-ZCode-App-Version": "3.10.2",
		"X-Release-Channel":   "stable",
	}
	for k, want := range checks {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if h.Get("X-Platform") == "" {
		t.Error("X-Platform should be set")
	}
	if h.Get("X-Os-Category") == "" {
		t.Error("X-Os-Category should be set")
	}
}

// TestPrepareZcodeRequestPinsBaseURL verifies the base URL is pinned to
// api.z.ai and ZCode source headers are injected into opts.Headers.
func TestPrepareZcodeRequestPinsBaseURL(t *testing.T) {
	e := NewZcodeExecutor(nil)
	auth := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{"api_key": "key-1.secret"}}
	opts := cliproxyexecutor.Options{}

	auth2, opts2 := e.prepareZcodeRequest(auth, opts)

	if auth2.Attributes["base_url"] != ZCodeAnthropicBaseURL {
		t.Errorf("base_url = %q, want %q", auth2.Attributes["base_url"], ZCodeAnthropicBaseURL)
	}
	if opts2.Headers.Get("HTTP-Referer") != "https://zcode.z.ai" {
		t.Errorf("HTTP-Referer not injected: %q", opts2.Headers.Get("HTTP-Referer"))
	}
	if opts2.Headers.Get("X-ZCode-Agent") != "glm" {
		t.Errorf("X-ZCode-Agent not injected: %q", opts2.Headers.Get("X-ZCode-Agent"))
	}
	// Original auth must not be mutated.
	if auth.Attributes["base_url"] != "" {
		t.Errorf("original auth mutated: base_url = %q", auth.Attributes["base_url"])
	}
}

// TestZcodeCreds verifies the provisioned key is read from api_key.
func TestZcodeCreds(t *testing.T) {
	if got := zcodeCreds(&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "  key-1.secret  "}}); got != "key-1.secret" {
		t.Errorf("zcodeCreds = %q, want key-1.secret", got)
	}
	if got := zcodeCreds(nil); got != "" {
		t.Errorf("zcodeCreds(nil) = %q, want empty", got)
	}
}

// TestMergeHeaders verifies extra headers win over base.
func TestMergeHeaders(t *testing.T) {
	base := http.Header{}
	base.Set("X-ZCode-Agent", "old")
	extra := http.Header{}
	extra.Set("X-ZCode-Agent", "glm")
	merged := mergeHeaders(base, extra)
	if merged.Get("X-ZCode-Agent") != "glm" {
		t.Errorf("merged X-ZCode-Agent = %q, want glm", merged.Get("X-ZCode-Agent"))
	}
}

// TestPrepareZcodeRequestAlwaysUsesProvisionedKey pins the gajae-code contract:
// every zcode request authenticates with the provisioned Z.AI API key against
// api.z.ai.
//
// Routing through the zcode.z.ai start-plan gateway was implemented and then
// removed. That origin accepts the broker JWT as a credential but rejects model
// requests with {"code":3007,"msg":"captcha verify failed"}, because it
// requires the ZCode desktop app's client attestation, which this proxy cannot
// produce. A stored broker JWT must therefore never divert the request or
// replace the credential.
func TestPrepareZcodeRequestAlwaysUsesProvisionedKey(t *testing.T) {
	e := NewZcodeExecutor(nil)

	cases := []struct {
		name string
		auth *cliproxyauth.Auth
	}{
		{
			name: "broker jwt present",
			auth: &cliproxyauth.Auth{
				Provider:   "zcode",
				Attributes: map[string]string{"api_key": "key-1.secret", "zcode_token": "jwt-broker-1"},
			},
		},
		{
			name: "legacy start-plan attributes",
			auth: &cliproxyauth.Auth{
				Provider: "zcode",
				Attributes: map[string]string{
					"api_key":           "key-1.secret",
					"zcode_token":       "jwt-broker-1",
					"start_plan_active": "true",
				},
			},
		},
		{
			name: "legacy start-plan metadata on reloaded record",
			auth: &cliproxyauth.Auth{
				Provider: "zcode",
				Metadata: map[string]any{
					"access_token": "key-1.secret",
					"zcode_token":  "jwt-meta",
					"start_plan":   true,
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth2, opts2 := e.prepareZcodeRequest(tc.auth, cliproxyexecutor.Options{})

			gotKey, gotBase := claudeCreds(auth2)
			if gotBase != ZCodeAnthropicBaseURL {
				t.Errorf("base_url = %q, want %q", gotBase, ZCodeAnthropicBaseURL)
			}
			if gotKey != "key-1.secret" {
				t.Errorf("credential = %q, want the provisioned Z.AI key", gotKey)
			}
			if got := opts2.Headers.Get("Authorization"); got != "" {
				t.Errorf("Authorization should not be preset, got %q", got)
			}
			if got := opts2.Headers.Get("x-api-key"); got != "" {
				t.Errorf("x-api-key should not be preset, got %q", got)
			}
		})
	}
}

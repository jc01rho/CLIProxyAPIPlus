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

	auth2, _ := e.prepareZcodeRequest(auth, opts)

	if auth2.Attributes["base_url"] != ZCodeAnthropicBaseURL {
		t.Errorf("base_url = %q, want %q", auth2.Attributes["base_url"], ZCodeAnthropicBaseURL)
	}
	if auth2.Attributes["header:Http-Referer"] != "https://zcode.z.ai" {
		t.Errorf("HTTP-Referer not injected: %q", auth2.Attributes["header:Http-Referer"])
	}
	if auth2.Attributes["header:X-Zcode-Agent"] != "glm" {
		t.Errorf("X-ZCode-Agent not injected: %q", auth2.Attributes["header:X-Zcode-Agent"])
	}
	if auth2.Attributes["header:X-Session-Id"] == "" {
		t.Error("X-Session-Id not injected")
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

// TestPrepareZcodeRequestRouting pins the restored start plan routing:
//   - a broker JWT WITHOUT an active start plan stays on api.z.ai with the
//     provisioned Z.AI key (gajae-code contract, safe default);
//   - a broker JWT WITH an active start plan routes through the zcode.z.ai
//     gateway with the broker JWT in the credential slot, so start plan quota
//     is consumed instead of the individual plan;
//   - the X-Session-Id identity header is always attached.
func TestPrepareZcodeRequestRouting(t *testing.T) {
	e := NewZcodeExecutor(nil)

	cases := []struct {
		name      string
		auth      *cliproxyauth.Auth
		wantBase  string
		wantCreds string
	}{
		{
			name: "broker jwt without start plan stays on api.z.ai",
			auth: &cliproxyauth.Auth{
				Provider:   "zcode",
				Attributes: map[string]string{"api_key": "key-1.secret", "zcode_token": "jwt-broker-1"},
			},
			wantBase:  ZCodeAnthropicBaseURL,
			wantCreds: "key-1.secret",
		},
		{
			name: "active start plan routes through the gateway",
			auth: &cliproxyauth.Auth{
				Provider: "zcode",
				Attributes: map[string]string{
					"api_key":           "key-1.secret",
					"zcode_token":       "jwt-broker-1",
					"start_plan_active": "true",
				},
			},
			wantBase:  ZCodeStartPlanBaseURL,
			wantCreds: "jwt-broker-1",
		},
		{
			name: "metadata start plan on reloaded record routes",
			auth: &cliproxyauth.Auth{
				Provider: "zcode",
				Metadata: map[string]any{
					"access_token":      "key-1.secret",
					"zcode_token":       "jwt-meta",
					"start_plan_active": true,
				},
			},
			wantBase:  ZCodeStartPlanBaseURL,
			wantCreds: "jwt-meta",
		},
		{
			name: "legacy start_plan metadata routes",
			auth: &cliproxyauth.Auth{
				Provider: "zcode",
				Metadata: map[string]any{
					"access_token": "key-1.secret",
					"zcode_token":  "jwt-legacy",
					"start_plan":   true,
				},
			},
			wantBase:  ZCodeStartPlanBaseURL,
			wantCreds: "jwt-legacy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth2, opts2 := e.prepareZcodeRequest(tc.auth, cliproxyexecutor.Options{})

			gotKey, gotBase := claudeCreds(auth2)
			if gotBase != tc.wantBase {
				t.Errorf("base_url = %q, want %q", gotBase, tc.wantBase)
			}
			if gotKey != tc.wantCreds {
				t.Errorf("credential = %q, want %q", gotKey, tc.wantCreds)
			}
			if got := opts2.Headers.Get("Authorization"); got != "" {
				t.Errorf("Authorization should not be preset, got %q", got)
			}
			if got := opts2.Headers.Get("x-api-key"); got != "" {
				t.Errorf("x-api-key should not be preset, got %q", got)
			}
			if got := auth2.Attributes["header:X-Session-Id"]; got == "" {
				t.Error("X-Session-Id identity attr missing")
			}
			if got := auth2.Attributes["header:User-Agent"]; got != "ZCode/"+zcodeAppVersion {
				t.Errorf("User-Agent attr = %q", got)
			}
			if got := auth2.Attributes["header:X-Zcode-Agent"]; got != "glm" {
				t.Errorf("X-Zcode-Agent attr = %q", got)
			}
			// Original auth must not be mutated.
			if tc.auth.Attributes != nil && tc.auth.Attributes["base_url"] != "" {
				t.Errorf("original auth mutated: base_url = %q", tc.auth.Attributes["base_url"])
			}
		})
	}
}

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
		"User-Agent":          "ZCode/3.10.1",
		"HTTP-Referer":        "https://zcode.z.ai",
		"X-Title":             "Z Code@electron",
		"X-ZCode-Agent":       "glm",
		"X-ZCode-App-Version": "3.10.1",
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

// TestPrepareZcodeRequestRoutesStartPlan verifies that when an active start plan
// is recorded together with a broker JWT, the request is routed through the
// verified zcode-plan gateway (zcode.z.ai/api/v1/zcode-plan/anthropic, from
// app.asar buildZCodeEndpointUrls) with the JWT in both Authorization and
// x-api-key headers (app.asar buildAnthropicConnectivityAuthHeaders), so start
// plan quota is consumed instead of the individual plan.
func TestPrepareZcodeRequestRoutesStartPlan(t *testing.T) {
	e := NewZcodeExecutor(nil)
	auth := &cliproxyauth.Auth{
		Provider: "zcode",
		Attributes: map[string]string{
			"api_key":          "key-1.secret",
			"zcode_token":      "jwt-broker-1",
			"start_plan_active": "true",
		},
	}
	opts := cliproxyexecutor.Options{}

	auth2, opts2 := e.prepareZcodeRequest(auth, opts)

	if auth2.Attributes["base_url"] != ZCodeStartPlanBaseURL {
		t.Errorf("base_url = %q, want %q", auth2.Attributes["base_url"], ZCodeStartPlanBaseURL)
	}
	if got := opts2.Headers.Get("Authorization"); got != "Bearer jwt-broker-1" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer jwt-broker-1")
	}
	if got := opts2.Headers.Get("x-api-key"); got != "jwt-broker-1" {
		t.Errorf("x-api-key = %q, want jwt-broker-1", got)
	}
	if opts2.Headers.Get("HTTP-Referer") != "https://zcode.z.ai" {
		t.Errorf("HTTP-Referer not injected: %q", opts2.Headers.Get("HTTP-Referer"))
	}
	if got := opts2.Headers.Get("X-ZCode-App-Version"); got != "3.10.1" {
		t.Errorf("X-ZCode-App-Version = %q, want 3.10.1", got)
	}
	// Original auth must not be mutated.
	if auth.Attributes["base_url"] != "" {
		t.Errorf("original auth mutated: base_url = %q", auth.Attributes["base_url"])
	}
}

// TestPrepareZcodeRequestFallsBackWithoutStartPlan verifies the request falls
// back to api.z.ai when the start plan is inactive or no broker token is stored.
func TestPrepareZcodeRequestFallsBackWithoutStartPlan(t *testing.T) {
	e := NewZcodeExecutor(nil)

	cases := []struct {
		name string
		auth *cliproxyauth.Auth
	}{
		{
			name: "start_plan_active=false",
			auth: &cliproxyauth.Auth{
				Provider:   "zcode",
				Attributes: map[string]string{"api_key": "k", "zcode_token": "jwt", "start_plan_active": "false"},
			},
		},
		{
			name: "missing zcode_token",
			auth: &cliproxyauth.Auth{
				Provider:   "zcode",
				Attributes: map[string]string{"api_key": "k", "start_plan_active": "true"},
			},
		},
		{
			name: "no attributes",
			auth: &cliproxyauth.Auth{Provider: "zcode"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth2, opts2 := e.prepareZcodeRequest(tc.auth, cliproxyexecutor.Options{})
			if auth2.Attributes["base_url"] != ZCodeAnthropicBaseURL {
				t.Errorf("base_url = %q, want %q", auth2.Attributes["base_url"], ZCodeAnthropicBaseURL)
			}
			if opts2.Headers.Get("Authorization") != "" {
				t.Errorf("Authorization should be empty, got %q", opts2.Headers.Get("Authorization"))
			}
			if opts2.Headers.Get("x-api-key") != "" {
				t.Errorf("x-api-key should be empty, got %q", opts2.Headers.Get("x-api-key"))
			}
		})
	}
}

// TestZcodeUseStartPlanMetadataFallback verifies start-plan routing also
// honors the Metadata["start_plan"] flag set by older records that have not
// been re-provisioned.
func TestZcodeUseStartPlanMetadataFallback(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "zcode",
		Metadata: map[string]any{
			"start_plan":   true,
			"zcode_token":  "jwt-meta",
		},
	}
	if !zcodeUseStartPlan(auth) {
		t.Error("expected start plan from Metadata fallback")
	}
	auth2 := &cliproxyauth.Auth{Provider: "zcode", Metadata: map[string]any{"start_plan": false}}
	if zcodeUseStartPlan(auth2) {
		t.Error("expected no start plan when Metadata flag is false")
	}
}

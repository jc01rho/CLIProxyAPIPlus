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
// app.asar buildZCodeEndpointUrls) authenticating with the broker JWT, so start
// plan quota is consumed instead of the individual plan.
//
// The JWT travels in the credential slot, matching the app's zaiStartPlan
// provider entry (app.asar loadPresetProviders: apiKey = the zcode JWT).
// Carrying it as an opts.Header instead is silently discarded, because
// ClaudeExecutor rewrites Authorization/x-api-key from claudeCreds(auth).
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
	if got := auth2.Attributes["api_key"]; got != "jwt-broker-1" {
		t.Errorf("api_key = %q, want the broker JWT %q", got, "jwt-broker-1")
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
			if got := auth2.Attributes["api_key"]; got == "jwt" {
				t.Error("broker JWT must not authenticate the api.z.ai fallback path")
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

// TestPrepareZcodeRequestReplacesCredentialWithBrokerJWT verifies that
// start-plan routing swaps the credential the Claude request path actually
// authenticates with. Verified from the ZCode desktop app (app.asar
// loadPresetProviders): the zaiStartPlan provider entry is built with
// "apiKey: p" where p is loadZaiProviderConnectionZcodeJwtToken() — the broker
// JWT occupies the API-key slot, it is not an extra header.
//
// ClaudeExecutor resolves credentials through claudeCreds(auth) and
// unconditionally rewrites Authorization/x-api-key from it
// (applyClaudeHeadersWithNativeProfile), so a broker JWT injected only into
// opts.Headers is overwritten by the provisioned Z.AI key and the gateway
// answers 401.
func TestPrepareZcodeRequestReplacesCredentialWithBrokerJWT(t *testing.T) {
	e := NewZcodeExecutor(nil)
	auth := &cliproxyauth.Auth{
		Provider: "zcode",
		Attributes: map[string]string{
			"api_key":           "key-1.secret",
			"zcode_token":       "jwt-broker-1",
			"start_plan_active": "true",
		},
	}

	auth2, _ := e.prepareZcodeRequest(auth, cliproxyexecutor.Options{})

	gotKey, gotBase := claudeCreds(auth2)
	if gotBase != ZCodeStartPlanBaseURL {
		t.Errorf("claudeCreds base_url = %q, want %q", gotBase, ZCodeStartPlanBaseURL)
	}
	if gotKey != "jwt-broker-1" {
		t.Errorf("claudeCreds api_key = %q, want the broker JWT %q", gotKey, "jwt-broker-1")
	}
	// The provisioned Z.AI key must remain available for the non-start-plan
	// fallback, so it may not be destroyed on the original record.
	if auth.Attributes["api_key"] != "key-1.secret" {
		t.Errorf("original auth mutated: api_key = %q", auth.Attributes["api_key"])
	}
}

// TestPrepareZcodeRequestKeepsProvisionedKeyWithoutStartPlan verifies the
// non-start-plan path still authenticates with the provisioned Z.AI API key.
func TestPrepareZcodeRequestKeepsProvisionedKeyWithoutStartPlan(t *testing.T) {
	e := NewZcodeExecutor(nil)
	auth := &cliproxyauth.Auth{
		Provider:   "zcode",
		Attributes: map[string]string{"api_key": "key-1.secret", "zcode_token": "jwt-broker-1"},
	}

	auth2, _ := e.prepareZcodeRequest(auth, cliproxyexecutor.Options{})

	gotKey, gotBase := claudeCreds(auth2)
	if gotBase != ZCodeAnthropicBaseURL {
		t.Errorf("claudeCreds base_url = %q, want %q", gotBase, ZCodeAnthropicBaseURL)
	}
	if gotKey != "key-1.secret" {
		t.Errorf("claudeCreds api_key = %q, want key-1.secret", gotKey)
	}
}

// TestPrepareZcodeRequestStartPlanFallsBackToMetadataKey verifies a record
// reloaded from disk (flat TokenStorage form, nil Attributes) still routes the
// broker JWT into the credential slot.
func TestPrepareZcodeRequestStartPlanFallsBackToMetadataKey(t *testing.T) {
	e := NewZcodeExecutor(nil)
	auth := &cliproxyauth.Auth{
		Provider: "zcode",
		Metadata: map[string]any{
			"access_token": "key-1.secret",
			"zcode_token":  "jwt-meta",
			"start_plan":   true,
		},
	}

	auth2, _ := e.prepareZcodeRequest(auth, cliproxyexecutor.Options{})

	gotKey, gotBase := claudeCreds(auth2)
	if gotBase != ZCodeStartPlanBaseURL {
		t.Errorf("claudeCreds base_url = %q, want %q", gotBase, ZCodeStartPlanBaseURL)
	}
	if gotKey != "jwt-meta" {
		t.Errorf("claudeCreds api_key = %q, want jwt-meta", gotKey)
	}
}

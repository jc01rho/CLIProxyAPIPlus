package executor

import (
	ed25519 "crypto/ed25519"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestBuildZCodeSourceHeaders verifies the ZCode source headers match the
// gajae-code buildZCodeSourceHeaders contract.
func TestBuildZCodeSourceHeaders(t *testing.T) {
	h := buildZCodeSourceHeaders()
	checks := map[string]string{
		"User-Agent":          "ZCode/3.11.2",
		"HTTP-Referer":        "https://zcode.z.ai",
		"X-Title":             "Z Code@electron",
		"X-ZCode-Agent":       "glm",
		"X-ZCode-App-Version": "3.11.2",
		"X-Release-Channel":   "production",
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

// TestPrepareZcodeRequestPinsBaseURL verifies the base URL is pinned to the
// ultra gateway (extension parity default) and ZCode source headers are
// injected into auth header: attributes.
func TestPrepareZcodeRequestPinsBaseURL(t *testing.T) {
	e := NewZcodeExecutor(nil)
	auth := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{"api_key": "key-1.secret"}}
	opts := cliproxyexecutor.Options{}

	auth2, _, _ := e.prepareZcodeRequest(auth, cliproxyexecutor.Request{}, opts)

	if auth2.Attributes["base_url"] != ZCodeUltraBaseURL {
		t.Errorf("base_url = %q, want %q", auth2.Attributes["base_url"], ZCodeUltraBaseURL)
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

// TestPrepareZcodeRequestRouting pins the routing behavior (extension parity):
//   - the DEFAULT path (no active start plan) goes to the ultra gateway —
//     the destination the desktop's proxyEndpoint.mapping resolves for signed
//     traffic — with the provisioned Z.AI key in the credential slot;
//   - a broker JWT WITH an active start plan routes through the zcode.z.ai
//     start-plan gateway with the broker JWT, so start plan quota is consumed
//     instead of the individual plan (unchanged local balance-gated feature);
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
			name: "no start plan routes through the ultra gateway",
			auth: &cliproxyauth.Auth{
				Provider:   "zcode",
				Attributes: map[string]string{"api_key": "key-1.secret", "zcode_token": "jwt-broker-1"},
			},
			wantBase:  ZCodeUltraBaseURL,
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
			auth2, _, _ := e.prepareZcodeRequest(tc.auth, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})

			gotKey, gotBase := claudeCreds(auth2)
			if gotBase != tc.wantBase {
				t.Errorf("base_url = %q, want %q", gotBase, tc.wantBase)
			}
			if gotKey != tc.wantCreds {
				t.Errorf("credential = %q, want %q", gotKey, tc.wantCreds)
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

// zcodeTestStatusErr is a minimal error carrying a status code and body,
// mirroring the statusErr shape returned by classifyClaudeUpstreamError.
type zcodeTestStatusErr struct {
	code int
	msg  string
}

func (e zcodeTestStatusErr) Error() string   { return e.msg }
func (e zcodeTestStatusErr) StatusCode() int { return e.code }
func (e zcodeTestStatusErr) Body() []byte    { return []byte(e.msg) }

// TestZcodeSignatureRejected verifies the 401+reason predicate.
func TestZcodeSignatureRejected(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{"api_key": "k.s"}}
	if zcodeSignatureRejected(auth, nil) {
		t.Error("nil error must not be treated as a rejection")
	}
	if !zcodeSignatureRejected(auth, zcodeTestStatusErr{code: 401, msg: `{"msg":"VERIFY_SIGNATURE_INVALID"}`}) {
		t.Error("401 VERIFY_SIGNATURE_INVALID should be a rejection")
	}
	if !zcodeSignatureRejected(auth, zcodeTestStatusErr{code: 401, msg: `{"error":{"reason":"VERIFY_APIKEY_EXPIRED"}}`}) {
		t.Error("401 VERIFY_APIKEY_EXPIRED should be a rejection")
	}
	if zcodeSignatureRejected(auth, zcodeTestStatusErr{code: 401, msg: `{"msg":"VERIFY_CAPTCHA_FAILED"}`}) {
		t.Error("other 401 reasons are not signing rejections")
	}
	if zcodeSignatureRejected(auth, zcodeTestStatusErr{code: 403, msg: `{"msg":"VERIFY_SIGNATURE_INVALID"}`}) {
		t.Error("non-401 statuses are not signing rejections")
	}
}

// TestZcodeInvalidateAndBypass verifies the desktop retry ladder state
// transitions: invalidate drops only the key; bypass latches the credential.
func TestZcodeInvalidateAndBypass(t *testing.T) {
	zcodeResetSigningStateForTests()
	auth := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{"api_key": "k1.s"}}
	// Prime caches.
	zcodeSigningMu.Lock()
	zcodePrivateKeys["k1.s"] = ed25519.PrivateKey{}
	zcodeBypassSigning["k1.s"] = false
	zcodeSigningMu.Unlock()

	zcodeInvalidateSigningKey(auth)
	zcodeSigningMu.Lock()
	_, keyAlive := zcodePrivateKeys["k1.s"]
	bypassed := zcodeBypassSigning["k1.s"]
	zcodeSigningMu.Unlock()
	if keyAlive {
		t.Error("invalidate must drop the cached key")
	}
	if bypassed {
		t.Error("invalidate must not latch bypass")
	}

	zcodeDisableSigning(auth)
	zcodeSigningMu.Lock()
	_, keyAlive = zcodePrivateKeys["k1.s"]
	bypassed = zcodeBypassSigning["k1.s"]
	zcodeSigningMu.Unlock()
	if keyAlive {
		t.Error("bypass must also drop the key")
	}
	if !bypassed {
		t.Error("bypass must latch the credential")
	}
	zcodeResetSigningStateForTests()
}

// TestZcodeResetSigningStateForTests verifies the reset helper.
func TestZcodeResetSigningStateForTests(t *testing.T) {
	zcodeSigningMu.Lock()
	zcodeGateState = zcodeSignGate{enabled: true, checkedAt: time.Now()}
	zcodePrivateKeys["x"] = ed25519.PrivateKey{}
	zcodeBypassSigning["x"] = true
	zcodeSigningMu.Unlock()
	zcodeResetSigningStateForTests()
	zcodeSigningMu.Lock()
	defer zcodeSigningMu.Unlock()
	if zcodeGateState.enabled || len(zcodePrivateKeys) != 0 || len(zcodeBypassSigning) != 0 {
		t.Error("reset must clear gate, keys, and bypass maps")
	}
}

// --- off-peak routing tests (executor layer) ---

// TestZcodeOffPeakDisabledByDefault verifies the opt-in gate.
func TestZcodeOffPeakDisabledByDefault(t *testing.T) {
	zcodeResetSigningStateForTests()
	t.Setenv("ZCODE_OFFPEAK_ENABLE", "")
	auth := &cliproxyauth.Auth{
		Provider: "zcode",
		Attributes: map[string]string{
			"api_key":     "id.secret",
			"zcode_token": "jwt-1",
		},
	}
	if zcodeOffPeakActive(auth, "glm-5.3-flash") {
		t.Error("off-peak must be inactive without ZCODE_OFFPEAK_ENABLE=1")
	}
}

// TestZcodeOffPeakRequiresWindowFlashAndJWT verifies the routing predicate.
func TestZcodeOffPeakRequiresWindowFlashAndJWT(t *testing.T) {
	zcodeResetSigningStateForTests()
	t.Setenv("ZCODE_OFFPEAK_ENABLE", "1")

	// No broker JWT: off.
	authNoJWT := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{"api_key": "id.secret"}}
	if zcodeOffPeakActive(authNoJWT, "glm-5.3-flash") {
		t.Error("off-peak requires a broker JWT")
	}

	// JWT present but non-flash model: off (window-independent checks run
	// before the network call).
	authJWT := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{
		"api_key": "id.secret", "zcode_token": "jwt-1",
	}}
	if zcodeOffPeakActive(authJWT, "glm-5.3") {
		t.Error("off-peak only routes flash models")
	}
}

// TestZcodeOffPeakRoutingOverridesStartPlanAndDirect verifies that the
// off-peak decision only fires on the ultra branch: start-plan routing and
// ZCODE_ANTHROPIC_BASE_URL override both keep the request off the off-peak
// path.
func TestZcodeOffPeakRoutingOverridesStartPlanAndDirect(t *testing.T) {
	zcodeResetSigningStateForTests()
	t.Setenv("ZCODE_OFFPEAK_ENABLE", "1")
	e := NewZcodeExecutor(nil)

	// Start plan wins: base stays the start-plan gateway.
	authStartPlan := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{
		"api_key": "id.secret", "zcode_token": "jwt-1", "start_plan_active": "true",
	}}
	auth2, _, _ := e.prepareZcodeRequest(authStartPlan, cliproxyexecutor.Request{Model: "glm-5.3-flash"}, cliproxyexecutor.Options{})
	if auth2.Attributes["base_url"] != ZCodeStartPlanBaseURL {
		t.Errorf("start plan base = %q, want %q", auth2.Attributes["base_url"], ZCodeStartPlanBaseURL)
	}

	// Direct override wins: base is the override, never off-peak.
	t.Setenv("ZCODE_ANTHROPIC_BASE_URL", "https://example.internal/api/anthropic")
	authDirect := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{
		"api_key": "id.secret", "zcode_token": "jwt-1",
	}}
	auth3, _, _ := e.prepareZcodeRequest(authDirect, cliproxyexecutor.Request{Model: "glm-5.3-flash"}, cliproxyexecutor.Options{})
	if auth3.Attributes["base_url"] != "https://example.internal/api/anthropic" {
		t.Errorf("override base = %q", auth3.Attributes["base_url"])
	}
	t.Setenv("ZCODE_ANTHROPIC_BASE_URL", "")
}

// TestZcodeOffPeakTicketHeaders verifies the off-peak header install strips
// signature headers and carries the ticket + API key.
func TestZcodeOffPeakTicketHeaders(t *testing.T) {
	zcodeResetSigningStateForTests()
	zcode.OffPeakTicketBaseURL = "invalid://offline" // any ticket attempt fails open
	defer func() { zcode.OffPeakTicketBaseURL = "https://zcode.z.ai/api/v1/off-peak/ticket" }()

	auth := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{
		"api_key":     "id.secret",
		"zcode_token": "jwt-1",
	}}
	zcodeAttachOffPeakTicket(auth)
	// Offline ticket: fail-open means no ticket header, no panic.
	if got := auth.Attributes["header:X-Off-Peak-Ticket-ID"]; got != "" {
		t.Errorf("offline ticket must not install a ticket id, got %q", got)
	}
	if got := auth.Attributes["header:X-Coding-Plan-Api-Key"]; got != "" {
		t.Errorf("offline ticket must not install the api key header, got %q", got)
	}
}

// TestZcodeSignatureHeaderNames verifies the strip list matches the extension
// SIGNATURE_HEADERS contract.
func TestZcodeSignatureHeaderNames(t *testing.T) {
	want := []string{"X-Client-Ts", "X-Client-Version", "X-Client-Sig", "X-Client-Nonce", "X-Client-Pow", "X-App-Id", "X-Client-Sign-Verified"}
	if len(zcodeSignatureHeaderNames) != len(want) {
		t.Fatalf("strip list length = %d, want %d", len(zcodeSignatureHeaderNames), len(want))
	}
	for i, name := range want {
		if zcodeSignatureHeaderNames[i] != name {
			t.Errorf("strip list[%d] = %q, want %q", i, zcodeSignatureHeaderNames[i], name)
		}
	}
}

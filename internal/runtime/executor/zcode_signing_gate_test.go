package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestZcodeSigningGateCacheIsPerCredential verifies the feature-gate answer is
// cached per credential (extension parity: per-API-key states map) and a cold
// credential whose probe fails still signs (fail-open) without hanging.
func TestZcodeSigningGateCacheIsPerCredential(t *testing.T) {
	zcodeResetSigningStateForTests()
	defer zcodeResetSigningStateForTests()

	zcodeSigningMu.Lock()
	zcodeGateStates["warm-credential"] = zcodeSignGate{enabled: true, checkedAt: time.Now()}
	zcodeSigningMu.Unlock()

	if !zcodeSigningEnabled(context.Background(), "warm-credential") {
		t.Error("a cached affirmative gate answer must be reused")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if !zcodeSigningEnabled(cancelled, "cold-credential") {
		t.Error("a failed gate probe is no answer and must still sign")
	}
	if !zcodeSigningEnabled(cancelled, "warm-credential") {
		t.Error("another credential's negative probe must not evict the cached answer")
	}
	zcodeSigningMu.Lock()
	_, cached := zcodeGateStates["cold-credential"]
	zcodeSigningMu.Unlock()
	if cached {
		t.Error("negative gate answers must not be cached")
	}
}

// TestZcodeSigningGateCacheExpires verifies the one-hour TTL is enforced.
func TestZcodeSigningGateCacheExpires(t *testing.T) {
	zcodeResetSigningStateForTests()
	defer zcodeResetSigningStateForTests()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"codingPlanSignature":{"enable":false}}}`))
	}))
	defer server.Close()
	prevClient := zcodeHTTPClient
	zcodeHTTPClient = &http.Client{Transport: redirectTransport{target: server.URL}}
	defer func() { zcodeHTTPClient = prevClient }()

	zcodeSigningMu.Lock()
	zcodeGateStates["stale-credential"] = zcodeSignGate{enabled: true, checkedAt: time.Now().Add(-2 * zcodeGateTTL)}
	zcodeSigningMu.Unlock()

	if zcodeSigningEnabled(context.Background(), "stale-credential") {
		t.Error("a stale gate answer must be re-probed, not reused")
	}
	zcodeSigningMu.Lock()
	_, cached := zcodeGateStates["stale-credential"]
	zcodeSigningMu.Unlock()
	if cached {
		t.Error("an explicit enable=false answer must drop the cached entry")
	}
}

// TestZcodeParseSigningGate pins the extension's gate decision table: only an
// explicit boolean false turns signing off, and only a code-0 answer that
// allows signing is cacheable.
func TestZcodeParseSigningGate(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantSign  bool
		wantCache bool
	}{
		{"explicit true", `{"code":0,"data":{"codingPlanSignature":{"enable":true}}}`, true, true},
		{"explicit false", `{"code":0,"data":{"codingPlanSignature":{"enable":false}}}`, false, false},
		{"flag absent", `{"code":0,"data":{}}`, true, true},
		{"data absent", `{"code":0}`, true, true},
		{"signature object without enable", `{"code":0,"data":{"codingPlanSignature":{}}}`, true, true},
		{"non-boolean enable", `{"code":0,"data":{"codingPlanSignature":{"enable":"no"}}}`, true, false},
		{"null enable", `{"code":0,"data":{"codingPlanSignature":{"enable":null}}}`, true, false},
		{"non-zero code", `{"code":500,"data":{"codingPlanSignature":{"enable":false}}}`, true, false},
		{"string code", `{"code":"0","data":{"codingPlanSignature":{"enable":false}}}`, true, false},
		{"malformed json", `{"code":0,`, true, false},
		{"empty body", ``, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sign, cache := zcodeParseSigningGate([]byte(tc.body))
			if sign != tc.wantSign || cache != tc.wantCache {
				t.Errorf("zcodeParseSigningGate(%s) = (sign %v, cache %v), want (%v, %v)", tc.body, sign, cache, tc.wantSign, tc.wantCache)
			}
		})
	}
}

// TestZcodeSigningGateSendsOnlySourceHeaders verifies the gate request carries
// the app's source headers and the key, but neither X-ZCode-Agent nor any
// per-request wire identity.
func TestZcodeSigningGateSendsOnlySourceHeaders(t *testing.T) {
	zcodeResetSigningStateForTests()
	defer zcodeResetSigningStateForTests()

	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = w.Write([]byte(`{"code":0,"data":{"codingPlanSignature":{"enable":false}}}`))
	}))
	defer server.Close()

	prevClient := zcodeHTTPClient
	zcodeHTTPClient = &http.Client{Transport: redirectTransport{target: server.URL}}
	defer func() { zcodeHTTPClient = prevClient }()

	if zcodeSigningEnabled(context.Background(), "gate-key.secret") {
		t.Fatal("an explicit enable=false answer must turn signing off")
	}
	if seen == nil {
		t.Fatal("gate endpoint was never called")
	}
	if got := seen.Get("X-Api-Key"); got != "gate-key.secret" {
		t.Errorf("x-api-key = %q, want the credential", got)
	}
	if got := seen.Get("X-Zcode-Agent"); got != "" {
		t.Errorf("X-ZCode-Agent = %q, must not reach the gate", got)
	}
	for _, name := range []string{"User-Agent", "Http-Referer", "X-Title", "X-Zcode-App-Version", "X-Release-Channel"} {
		if seen.Get(name) == "" {
			t.Errorf("%s missing from the gate request", name)
		}
	}
	for _, name := range []string{"Authorization", "Cookie", "X-Session-Id"} {
		if seen.Get(name) != "" {
			t.Errorf("%s must not reach the gate", name)
		}
	}
}

// redirectTransport sends every request to a local test server.
type redirectTransport struct{ target string }

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(r.target)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = u.Scheme
	clone.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(clone)
}

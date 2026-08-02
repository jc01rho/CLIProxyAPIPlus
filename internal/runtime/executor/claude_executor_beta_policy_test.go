package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudeRaceProbeOAuthKey = "sk-ant-oat-beta-policy"

func claudeOAuthAuthForBetaPolicy() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "claude-beta-policy",
		Metadata: map[string]any{"access_token": claudeRaceProbeOAuthKey},
	}
}

// A confirmed native client authenticates to CPA with the user's configured key
// and cannot know CPA will pick an OAuth credential upstream, so its header never
// carries the OAuth betas. Passing it through verbatim produced a Bearer request
// declaring neither of them.
func TestApplyClaudeHeaders_ConfirmedClientKeepsOAuthCredentialBetas(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", claudeCodeBeta+",interleaved-thinking-2025-05-14,"+claudeEffortBeta)

	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
		[]byte(`{"model":"claude-opus-5"}`), nil, incoming, true); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}

	got := req.Header.Get("Anthropic-Beta")
	parts := strings.Split(got, ",")
	if len(parts) < 2 || parts[0] != claudeCodeBeta || parts[1] != claudeOAuthBeta {
		t.Fatalf("Anthropic-Beta = %q, want %s at position 2", got, claudeOAuthBeta)
	}
	if parts[len(parts)-1] != claudeExtendedCacheTTLBeta {
		t.Fatalf("Anthropic-Beta = %q, want %s last", got, claudeExtendedCacheTTLBeta)
	}
	// The caller's own betas survive the restoration.
	for _, want := range []string{"interleaved-thinking-2025-05-14", claudeEffortBeta} {
		if !strings.Contains(got, want) {
			t.Fatalf("Anthropic-Beta = %q, want caller beta %s preserved", got, want)
		}
	}
}

func TestApplyClaudeHeaders_ConfirmedAPIKeyClientKeepsPurePassthrough(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", claudeCodeBeta+","+claudeEffortBeta)

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-passthrough"}}
	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, auth, "key-passthrough", false, nil,
		[]byte(`{"model":"claude-opus-5"}`), nil, incoming, true); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got, want := req.Header.Get("Anthropic-Beta"), claudeCodeBeta+","+claudeEffortBeta; got != want {
		t.Fatalf("Anthropic-Beta = %q, want untouched passthrough %q", got, want)
	}
}

// Betas lifted out of the body must obey the same policy as header-supplied ones.
// Anthropic rejects an unknown beta outright, so letting the body bypass the gate
// turned a caller-controlled field into a guaranteed 400.
func TestApplyClaudeHeaders_UnknownBodyBetaDroppedOnAnthropic(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-body-beta"}}
	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, auth, "key-body-beta", false, []string{"unknown-body-probe-2099-01-01"},
		[]byte(`{"model":"claude-opus-5"}`), nil, nil, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got := req.Header.Get("Anthropic-Beta"); strings.Contains(got, "unknown-body-probe-2099-01-01") {
		t.Fatalf("Anthropic-Beta = %q, want the unknown body beta dropped", got)
	}
}

func TestApplyClaudeHeaders_KnownBodyBetaStillPlacedOnAnthropic(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-known-body-beta"}}
	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, auth, "key-known-body-beta", false, []string{claudeContext1MBeta},
		[]byte(`{"model":"claude-opus-5"}`), nil, nil, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	got := req.Header.Get("Anthropic-Beta")
	parts := strings.Split(got, ",")
	if len(parts) < 2 || parts[1] != claudeContext1MBeta {
		t.Fatalf("Anthropic-Beta = %q, want %s honored at its captured position", got, claudeContext1MBeta)
	}
}

// Custom credential headers run after the whole header set is assembled, so they
// could rewrite the reconstructed identity on Anthropic itself.
func TestApplyClaudeHeaders_CustomHeadersCannotOverrideAnthropicIdentity(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":                "key-custom-headers",
		"header:Anthropic-Beta":  "attacker-controlled-2099-01-01",
		"header:Accept-Encoding": "identity",
	}}

	for _, stream := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, auth, "key-custom-headers", stream, nil,
			[]byte(`{"model":"claude-opus-5"}`), nil, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}
		if got := req.Header.Get("Anthropic-Beta"); got == "attacker-controlled-2099-01-01" {
			t.Fatalf("stream=%v: custom header overrode Anthropic-Beta", stream)
		}
		if got := req.Header.Get("Accept-Encoding"); got != "gzip, deflate, br, zstd" {
			t.Fatalf("stream=%v: Accept-Encoding = %q, want the negotiated transport", stream, got)
		}
	}
}

// Kimi rewrites base_url to api.kimi.com and custom gateways set their own host,
// yet both delegate to ClaudeExecutor and are therefore cloaked. Keying the
// context_management injection on the cloaked flag alone leaked a Claude Code
// field into their traffic.
func TestClaudeExecutor_ContextManagementNeverLeaksToOtherUpstreams(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "claude-non-anthropic-upstream",
		Attributes: map[string]string{"api_key": "sk-ant-oat-non-anthropic", "base_url": server.URL},
	}
	payload := []byte(`{"model":"claude-opus-5","system":"p","messages":[{"role":"user","content":"hi"}]}`)

	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(upstreamBody, "context_management"); got.Exists() {
		t.Fatalf("non-Anthropic upstream received context_management = %s", got.Raw)
	}
}

func TestIsAnthropicUpstreamBase(t *testing.T) {
	cases := map[string]bool{
		"https://api.anthropic.com":      true,
		"https://API.Anthropic.com":      true,
		"https://api.kimi.com":           false,
		"http://api.anthropic.com":       false,
		"https://api.anthropic.com.evil": false,
		"https://gateway.example.com":    false,
		"":                               false,
	}
	for base, want := range cases {
		if got := isAnthropicUpstreamBase(base); got != want {
			t.Fatalf("isAnthropicUpstreamBase(%q) = %v, want %v", base, got, want)
		}
	}
}

// Streaming previously never reached the fast-mode derivation, so speed:"fast"
// produced a 400 on every streamed request.
func TestApplyClaudeHeaders_FastModeBetaMatchesAcrossStreamModes(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-fast-parity"}}
	body := []byte(`{"model":"claude-opus-5","speed":"fast"}`)

	var seen []string
	for _, stream := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, auth, "key-fast-parity", stream, nil, body, nil, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}
		got := req.Header.Get("Anthropic-Beta")
		if !strings.Contains(got, claudeFastModeBeta) {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want %s", stream, got, claudeFastModeBeta)
		}
		seen = append(seen, got)
	}
	if seen[0] != seen[1] {
		t.Fatalf("stream and non-stream disagree:\n non-stream %q\n stream     %q", seen[0], seen[1])
	}
}

// extended-cache-ttl is the one measured trailing invariant; fast-mode has no
// captured position and must not displace it.
func TestApplyClaudeHeaders_FastModePrecedesOAuthTrailer(t *testing.T) {
	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, true, nil,
		[]byte(`{"model":"claude-opus-5","speed":"fast"}`), nil, nil, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	got := req.Header.Get("Anthropic-Beta")
	parts := strings.Split(got, ",")
	if parts[len(parts)-1] != claudeExtendedCacheTTLBeta {
		t.Fatalf("Anthropic-Beta = %q, want %s last", got, claudeExtendedCacheTTLBeta)
	}
	if parts[len(parts)-2] != claudeFastModeBeta {
		t.Fatalf("Anthropic-Beta = %q, want %s immediately before the OAuth trailer", got, claudeFastModeBeta)
	}
}

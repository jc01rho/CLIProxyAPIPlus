package executor

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestRetryClaudeFastModeRefusalMatchesNative220Fallback(t *testing.T) {
	t.Parallel()

	fastBody := []byte(strings.Replace(claudeCCH21220BaseBody, `"stream":true}`, `"speed":"fast","stream":true}`, 1))
	fastBody, errSign := finalizeAnthropicMessagesBodyCCH(fastBody, "")
	if errSign != nil {
		t.Fatal(errSign)
	}
	initialReq, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", bytes.NewReader(fastBody))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	initialReq.Header.Set("X-Claude-Code-Session-Id", "11111111-2222-4333-8444-555555555555")
	initialReq.Header.Set("x-client-request-id", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	initialResp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for fast mode."}}`)),
		Request:    initialReq,
	}

	var fallbackBody []byte
	var fallbackHeaders http.Header
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var errRead error
		fallbackBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		fallbackHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")),
			Request:    req,
		}, nil
	})}

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-ant-oat-fast-fallback"}}
	finalResp, gotBody, retried, errRetry := executor.retryClaudeFastModeRefusal(initialReq, client, initialResp, claudeFastFallbackOptions{
		auth:                     auth,
		apiKey:                   "sk-ant-oat-fast-fallback",
		stream:                   true,
		body:                     fastBody,
		cchSigning:               true,
		sessionID:                "11111111-2222-4333-8444-555555555555",
		allowEntitlementFallback: true,
	})
	if errRetry != nil {
		t.Fatalf("retryClaudeFastModeRefusal() error = %v", errRetry)
	}
	if !retried || finalResp.StatusCode != http.StatusOK {
		t.Fatalf("retried/status = %v/%d, want true/200", retried, finalResp.StatusCode)
	}
	if !bytes.Equal(gotBody, fallbackBody) {
		t.Fatal("returned fallback body differs from sent body")
	}
	if got := gjson.GetBytes(fallbackBody, "speed"); got.Exists() {
		t.Fatalf("fallback speed = %s, want absent", got.Raw)
	}
	if got := len(fastBody) - len(fallbackBody); got != 15 {
		t.Fatalf("fallback body length delta = %d, want 15", got)
	}
	beforeSystem := gjson.GetBytes(fastBody, "system.0.text").String()
	afterSystem := gjson.GetBytes(fallbackBody, "system.0.text").String()
	if beforeSystem == afterSystem {
		t.Fatal("Fast fallback did not recalculate the CCH-bearing system block")
	}
	resigned, errResign := finalizeAnthropicMessagesBodyCCH(fallbackBody, "")
	if errResign != nil {
		t.Fatal(errResign)
	}
	if !bytes.Equal(resigned, fallbackBody) {
		t.Fatal("fallback body CCH is not final")
	}
	if got := strings.Join(fallbackHeaders["anthropic-beta"], ","); !strings.Contains(got, claudeFastModeBeta) {
		t.Fatalf("fallback beta = %q, want Fast beta retained", got)
	}
	if got := fallbackHeaders.Get("X-Claude-Code-Session-Id"); got != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("fallback session ID = %q, want original session", got)
	}
	if got := strings.Join(fallbackHeaders["x-client-request-id"], ","); got == "" || got == initialReq.Header.Get("x-client-request-id") {
		t.Fatalf("fallback request ID = %q, want a new ID", got)
	}
	if got := fallbackHeaders.Get("X-Stainless-Retry-Count"); got != "0" {
		t.Fatalf("fallback retry count = %q, want 0", got)
	}
}

func TestRetryClaudeFastModeRefusalLeavesConfirmedNativeToRetry(t *testing.T) {
	t.Parallel()

	req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", strings.NewReader(`{"speed":"fast"}`))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Usage credits are required for fast mode."}}`)), Header: make(http.Header)}
	gotResp, _, retried, errRetry := NewClaudeExecutor(&config.Config{}).retryClaudeFastModeRefusal(req, http.DefaultClient, resp, claudeFastFallbackOptions{allowEntitlementFallback: false})
	if errRetry != nil {
		t.Fatal(errRetry)
	}
	if retried || gotResp != resp {
		t.Fatal("confirmed native refusal must be returned for the native client to retry")
	}
}

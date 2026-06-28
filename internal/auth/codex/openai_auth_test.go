package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"golang.org/x/sync/singleflight"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func resetCodexRefreshGroupForTest() {
	codexRefreshGroup = singleflight.Group{}
}

func TestGenerateAuthURLMatchesReferenceQueryOrder(t *testing.T) {
	auth := &CodexAuth{}
	got, err := auth.GenerateAuthURL("state value", &PKCECodes{CodeChallenge: "challenge/value"})
	if err != nil {
		t.Fatalf("GenerateAuthURL error: %v", err)
	}
	want := AuthURL + "?" + "response_type=code&client_id=" + ClientID + "&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback&scope=openid+profile+email+offline_access&code_challenge=challenge%2Fvalue&code_challenge_method=S256&id_token_add_organizations=true&codex_cli_simplified_flow=true&state=state+value&originator=opencode"
	if got != want {
		t.Fatalf("auth URL = %q, want %q", got, want)
	}
}

func TestExchangeAndRefreshBodiesMatchReferenceOrder(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)

	var gotExchangeBody string
	var gotRefreshBody string
	var calls int
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		calls++
		if calls == 1 {
			if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
				t.Fatalf("exchange Content-Type = %q", got)
			}
			gotExchangeBody = string(body)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"access","refresh_token":"refresh","id_token":"","expires_in":3600}`)), Header: make(http.Header), Request: req}, nil
		}
		gotRefreshBody = string(body)
		if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Fatalf("refresh Content-Type = %q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","id_token":"","expires_in":3600}`)), Header: make(http.Header), Request: req}, nil
	})}}

	if _, err := auth.ExchangeCodeForTokensWithRedirect(context.Background(), "code value", "http://localhost:1455/auth/callback", &PKCECodes{CodeVerifier: "verifier/value"}); err != nil {
		t.Fatalf("ExchangeCodeForTokensWithRedirect error: %v", err)
	}
	wantExchange := "grant_type=authorization_code&code=code+value&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback&client_id=" + ClientID + "&code_verifier=verifier%2Fvalue"
	if gotExchangeBody != wantExchange {
		t.Fatalf("exchange body = %q, want %q", gotExchangeBody, wantExchange)
	}

	if _, err := auth.RefreshTokens(context.Background(), "refresh value"); err != nil {
		t.Fatalf("RefreshTokens error: %v", err)
	}
	wantRefresh := "grant_type=refresh_token&refresh_token=refresh+value&client_id=" + ClientID
	if gotRefreshBody != wantRefresh {
		t.Fatalf("refresh body = %q, want %q", gotRefreshBody, wantRefresh)
	}
}

func TestOAuthErrorBodiesMaskSensitiveFields(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)

	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`refresh_token=secret-refresh&code_verifier=secret-verifier&error=invalid_grant`)), Header: make(http.Header), Request: req}, nil
	})}}

	_, err := auth.RefreshTokens(context.Background(), "refresh-token")
	if err == nil {
		t.Fatal("expected refresh error")
	}
	errText := err.Error()
	if strings.Contains(errText, "secret-refresh") || strings.Contains(errText, "secret-verifier") {
		t.Fatalf("error leaked sensitive value: %s", errText)
	}
	if !strings.Contains(errText, "secr...resh") || !strings.Contains(errText, "secr...fier") {
		t.Fatalf("error did not preserve masked diagnostics: %s", errText)
	}
}

func TestRefreshTokensWithRetry_NonRetryableOnlyAttemptsOnce(t *testing.T) {
	var calls int32
	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","code":"refresh_token_reused"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected error for non-retryable refresh failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "refresh_token_reused") {
		t.Fatalf("expected refresh_token_reused in error, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 refresh attempt, got %d", got)
	}
}

func TestRefreshTokens_DeduplicatesConcurrentRefreshAcrossInstances(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)

	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		once.Do(func() { close(started) })
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"access_token":"new-access",
				"refresh_token":"new-refresh",
				"token_type":"Bearer",
				"expires_in":3600
			}`)),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})
	authA := &CodexAuth{httpClient: &http.Client{Transport: transport}}
	authB := &CodexAuth{httpClient: &http.Client{Transport: transport}}

	results := make(chan *CodexTokenData, 2)
	errs := make(chan error, 2)
	runRefresh := func(auth *CodexAuth, launched chan<- struct{}) {
		if launched != nil {
			close(launched)
		}
		tokenData, errRefresh := auth.RefreshTokens(context.Background(), "shared-refresh-token")
		results <- tokenData
		errs <- errRefresh
	}

	go runRefresh(authA, nil)
	<-started

	secondLaunched := make(chan struct{})
	go runRefresh(authB, secondLaunched)
	<-secondLaunched
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected concurrent refresh to share a single upstream call, got %d", got)
	}
	close(release)

	for i := 0; i < 2; i++ {
		if errRefresh := <-errs; errRefresh != nil {
			t.Fatalf("expected refresh to succeed, got %v", errRefresh)
		}
		tokenData := <-results
		if tokenData == nil || tokenData.AccessToken != "new-access" || tokenData.RefreshToken != "new-refresh" {
			t.Fatalf("unexpected token data: %#v", tokenData)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected both refresh callers to share a single upstream call, got %d", got)
	}
}

func TestNewCodexAuthWithProxyURL_OverrideDirectDisablesProxy(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://proxy.example.com:8080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "direct")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", auth.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewCodexAuthWithProxyURL_OverrideProxyTakesPrecedence(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://global.example.com:8080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "http://override.example.com:8081")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", auth.httpClient.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errReq != nil {
		t.Fatalf("new request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy func: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://override.example.com:8081" {
		t.Fatalf("proxy URL = %v, want http://override.example.com:8081", proxyURL)
	}
}

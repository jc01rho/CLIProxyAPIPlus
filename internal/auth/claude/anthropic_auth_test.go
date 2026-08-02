package claude

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewAnthropicHttpClientDoesNotSetRequestTimeout(t *testing.T) {
	if got := NewAnthropicHttpClient(nil).Timeout; got != 0 {
		t.Fatalf("HTTP client timeout = %s, want zero", got)
	}
}

func TestRefreshTokens_UsesIndependentTimeout(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	var requestDeadline time.Time
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var ok bool
				requestDeadline, ok = req.Context().Deadline()
				if !ok {
					t.Fatal("refresh request has no deadline")
				}
				if errContext := req.Context().Err(); errContext != nil {
					t.Fatalf("refresh request context is already done: %v", errContext)
				}
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":"probe"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokens(callerCtx, "independent-timeout-token")
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if requestDeadline.IsZero() || !requestDeadline.After(time.Now()) {
		t.Fatalf("refresh deadline = %v, want a future deadline", requestDeadline)
	}
}

func TestExchangeCodeForTokensPersistsUpstreamAccountAndDevicePool(t *testing.T) {
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || req.URL.String() != TokenURL {
					t.Fatalf("token request = %s %s, want POST %s", req.Method, req.URL, TokenURL)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"access_token":"access",
						"refresh_token":"refresh",
						"token_type":"Bearer",
						"expires_in":3600,
						"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email_address":"user@example.com"},
						"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}
					}`)),
					Header:  make(http.Header),
					Request: req,
				}, nil
			}),
		},
	}

	bundle, errExchange := auth.ExchangeCodeForTokens(context.Background(), "code", "state", &PKCECodes{CodeVerifier: "verifier"})
	if errExchange != nil {
		t.Fatalf("ExchangeCodeForTokens() error = %v", errExchange)
	}
	if bundle.TokenData.AccountUUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account UUID = %q, want OAuth response account", bundle.TokenData.AccountUUID)
	}
	if bundle.TokenData.OrganizationUUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || bundle.TokenData.OrganizationName != "Example Org" {
		t.Fatalf("organization = %q/%q, want OAuth response organization", bundle.TokenData.OrganizationUUID, bundle.TokenData.OrganizationName)
	}
	if len(bundle.DeviceIDs) != ClaudeDevicePoolSize {
		t.Fatalf("device pool length = %d, want %d", len(bundle.DeviceIDs), ClaudeDevicePoolSize)
	}
	storage := auth.CreateTokenStorage(bundle)
	if storage.AccountUUID != bundle.TokenData.AccountUUID || storage.OrganizationUUID != bundle.TokenData.OrganizationUUID {
		t.Fatalf("storage account identity = %#v, want bundle identity", storage)
	}
	if len(storage.DeviceIDs) != ClaudeDevicePoolSize {
		t.Fatalf("storage device pool length = %d, want %d", len(storage.DeviceIDs), ClaudeDevicePoolSize)
	}
}

func TestRefreshTokensWithRetry_429BlocksImmediateReplay(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	var calls int32
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(`{"error":"rate_limited"}`)),
					Header:     http.Header{"Retry-After": []string{"60"}},
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected 429 refresh error")
	}
	if !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("expected status 429 in error, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 refresh attempt after 429, got %d", got)
	}

	_, err = auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected immediate blocked refresh error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected blocked retry to avoid a second refresh call, got %d attempts", got)
	}
	if blockedUntil := claudeRefreshBlockedUntil("dummy_refresh_token"); !blockedUntil.After(time.Now()) {
		t.Fatalf("expected blocked-until timestamp to be set, got %v", blockedUntil)
	}
}

func TestRefreshTokens_DeduplicatesConcurrentRefresh(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				once.Do(func() { close(started) })
				<-release
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"access_token":"new-access",
						"refresh_token":"new-refresh",
						"token_type":"Bearer",
						"expires_in":3600,
						"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email_address":"shared@example.com"},
						"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Shared Org"}
					}`)),
					Header:  make(http.Header),
					Request: req,
				}, nil
			}),
		},
	}

	results := make(chan *ClaudeTokenData, 2)
	errs := make(chan error, 2)
	runRefresh := func() {
		td, err := auth.RefreshTokens(context.Background(), "shared-refresh-token")
		results <- td
		errs <- err
	}

	go runRefresh()
	go runRefresh()

	<-started
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected concurrent refresh to share a single upstream call, got %d", got)
	}
	close(release)

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("expected refresh to succeed, got %v", err)
		}
		td := <-results
		if td == nil || td.AccessToken != "new-access" {
			t.Fatalf("expected refreshed access token, got %#v", td)
		}
		if td.AccountUUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
			t.Fatalf("account UUID = %q, want OAuth response account", td.AccountUUID)
		}
		if td.OrganizationUUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || td.OrganizationName != "Shared Org" {
			t.Fatalf("organization = %q/%q, want OAuth response organization", td.OrganizationUUID, td.OrganizationName)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 upstream refresh call, got %d", got)
	}
}

func TestFetchOAuthProfile(t *testing.T) {
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.String() != ProfileURL {
					t.Fatalf("profile request = %s %s, want GET %s", req.Method, req.URL, ProfileURL)
				}
				if got := req.Header.Get("Authorization"); got != "Bearer test-access" {
					t.Fatalf("Authorization = %q, want bearer token", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"user@example.com"},
						"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}
					}`)),
					Header:  make(http.Header),
					Request: req,
				}, nil
			}),
		},
	}

	profile, errProfile := auth.FetchOAuthProfile(context.Background(), "test-access")
	if errProfile != nil {
		t.Fatalf("FetchOAuthProfile() error = %v", errProfile)
	}
	if profile.Account.UUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || profile.Account.Email != "user@example.com" {
		t.Fatalf("account = %#v, want upstream profile account", profile.Account)
	}
	if profile.Organization.UUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || profile.Organization.Name != "Example Org" {
		t.Fatalf("organization = %#v, want upstream profile organization", profile.Organization)
	}
}

func TestUpdateTokenStoragePreservesAccountWhenRefreshOmitsIt(t *testing.T) {
	storage := &ClaudeTokenStorage{
		AccountUUID:      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		OrganizationUUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		OrganizationName: "Example Org",
	}
	(&ClaudeAuth{}).UpdateTokenStorage(storage, &ClaudeTokenData{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		Email:        "user@example.com",
		Expire:       "2099-01-01T00:00:00Z",
	})

	if storage.AccountUUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account UUID = %q, want preserved", storage.AccountUUID)
	}
	if storage.OrganizationUUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || storage.OrganizationName != "Example Org" {
		t.Fatalf("organization = %q/%q, want preserved", storage.OrganizationUUID, storage.OrganizationName)
	}
}

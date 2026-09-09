package alysis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInitiateDeviceFlowParsesGrant(t *testing.T) {
	var gotPath string
	var gotAPIKey, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("apikey")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:              "dc-1",
			UserCode:                "ABCD-EFGH",
			VerificationURL:         ProductSiteURL + "/activate",
			VerificationURLComplete: ProductSiteURL + "/activate?code=ABCD-EFGH",
			ExpiresIn:               900,
			Interval:                5,
		})
	}))
	defer server.Close()

	prev := SupabaseURL
	SupabaseURL = server.URL
	defer func() { SupabaseURL = prev }()

	grant, err := NewAuth().InitiateDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("InitiateDeviceFlow returned error: %v", err)
	}
	if grant.DeviceCode != "dc-1" || grant.UserCode != "ABCD-EFGH" {
		t.Fatalf("unexpected grant: %+v", grant)
	}
	if grant.ExpiresIn != 900 || grant.Interval != 5 {
		t.Fatalf("expected defaults preserved, got %+v", grant)
	}
	if gotPath != DeviceCodePath {
		t.Fatalf("unexpected request path %q", gotPath)
	}
	if gotAPIKey == "" || gotAuth != "Bearer "+gotAPIKey {
		t.Fatalf("anon key headers missing: apikey=%q auth=%q", gotAPIKey, gotAuth)
	}
}

func TestInitiateDeviceFlowFillsDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"dc-2","user_code":"AAAA-1111"}`))
	}))
	defer server.Close()

	prev := SupabaseURL
	SupabaseURL = server.URL
	defer func() { SupabaseURL = prev }()

	grant, err := NewAuth().InitiateDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("InitiateDeviceFlow returned error: %v", err)
	}
	if grant.ExpiresIn != 900 {
		t.Fatalf("expected expires_in default 900, got %d", grant.ExpiresIn)
	}
	if grant.Interval != 5 {
		t.Fatalf("expected interval default 5, got %d", grant.Interval)
	}
}

func TestInitiateDeviceFlowRejectsMissingCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"only-device"}`))
	}))
	defer server.Close()

	prev := SupabaseURL
	SupabaseURL = server.URL
	defer func() { SupabaseURL = prev }()

	if _, err := NewAuth().InitiateDeviceFlow(context.Background()); err == nil {
		t.Fatal("expected error when user_code is missing")
	}
}

func TestPollForTokenApproved(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = json.Marshal(map[string]string{"path": r.URL.Path})
		_, _ = w.Write([]byte(`{"status":"approved","key":"slk_test_key"}`))
	}))
	defer server.Close()

	prev := SupabaseURL
	SupabaseURL = server.URL
	defer func() { SupabaseURL = prev }()

	resp, err := NewAuth().PollForToken(context.Background(), "dc-3")
	if err != nil {
		t.Fatalf("PollForToken returned error: %v", err)
	}
	if resp.Status != "approved" || resp.Key != "slk_test_key" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	var sent map[string]string
	if err = json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request bookkeeping broken: %v", err)
	}
	if sent["path"] != DeviceTokenPath {
		t.Fatalf("unexpected poll path %q", sent["path"])
	}
}

func TestPollForTokenTerminalStatuses(t *testing.T) {
	cases := map[string]string{
		"denied":          "rejected",
		"expired":         "expired",
		"not_found":       "expired",
		"already_claimed": "expired",
	}
	for status, want := range cases {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
			}))
			defer server.Close()

			prev := SupabaseURL
			SupabaseURL = server.URL
			defer func() { SupabaseURL = prev }()

			_, err := NewAuth().PollForToken(context.Background(), "dc-4")
			if err == nil {
				t.Fatalf("expected error for status %q", status)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("status %q: expected error mentioning %q, got %v", status, want, err)
			}
		})
	}
}

func TestPollForTokenApprovedWithoutKeyFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"approved"}`))
	}))
	defer server.Close()

	prev := SupabaseURL
	SupabaseURL = server.URL
	defer func() { SupabaseURL = prev }()

	if _, err := NewAuth().PollForToken(context.Background(), "dc-5"); err == nil {
		t.Fatal("expected error when approved response has no key")
	}
}

func TestPollForTokenPendingKeepsPolling(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	}))
	defer server.Close()

	prev := SupabaseURL
	SupabaseURL = server.URL
	defer func() { SupabaseURL = prev }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel shortly after the first immediate probe so the poll loop
		// observes ctx cancellation instead of waiting for a full interval.
		<-ctx.Done()
	}()
	cancel()

	if _, err := NewAuth().PollForToken(ctx, "dc-6"); err == nil {
		t.Fatal("expected context error after cancellation")
	}
	if calls > 1 {
		t.Fatalf("expected at most the immediate probe before cancellation, got %d calls", calls)
	}
}

func TestPollForTokenRequiresDeviceCode(t *testing.T) {
	if _, err := NewAuth().PollForToken(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty device code")
	}
}

func TestCredentialFileName(t *testing.T) {
	if got := CredentialFileName("a@b.c"); got != "alysis-a@b.c.json" {
		t.Fatalf("unexpected filename %q", got)
	}
	if got := CredentialFileName(""); got != "alysis-account.json" {
		t.Fatalf("unexpected fallback filename %q", got)
	}
}

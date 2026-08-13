package kilo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPollForTokenApprovesOn200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device-auth/codes/test-code" {
			t.Errorf("path = %s, want /device-auth/codes/test-code", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"approved","token":"test-token","userEmail":"u@kilo.ai"}`))
	}))
	t.Cleanup(server.Close)

	defer swapBaseURL(t, server.URL)()

	status, err := NewKiloAuth().PollForToken(context.Background(), "test-code")
	if err != nil {
		t.Fatalf("PollForToken: %v", err)
	}
	if status == nil || status.Token != "test-token" {
		t.Fatalf("status = %#v, want token=test-token", status)
	}
	if status.UserEmail != "u@kilo.ai" {
		t.Errorf("userEmail = %q, want u@kilo.ai", status.UserEmail)
	}
}

func TestPollForTokenPendingOn202(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	defer swapBaseURL(t, server.URL)()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	status, err := NewKiloAuth().PollForToken(ctx, "test-code")
	if err == nil || status != nil {
		t.Fatalf("status = %#v err = %v, want timeout error and nil status", status, err)
	}
	if calls == 0 {
		t.Errorf("server received zero calls")
	}
}

func TestPollForTokenDeniesOn403(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	defer swapBaseURL(t, server.URL)()

	status, err := NewKiloAuth().PollForToken(context.Background(), "test-code")
	if err == nil || status != nil {
		t.Fatalf("status = %#v err = %v, want denied error", status, err)
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %v, want contains 'denied'", err)
	}
}

func TestPollForTokenExpiresOn410(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	t.Cleanup(server.Close)

	defer swapBaseURL(t, server.URL)()

	status, err := NewKiloAuth().PollForToken(context.Background(), "test-code")
	if err == nil || status != nil {
		t.Fatalf("status = %#v err = %v, want expired error", status, err)
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error = %v, want contains 'expired'", err)
	}
}

// swapBaseURL redirects BaseURL to a local test server and restores it on
// the returned cleanup so tests never touch the real Kilo endpoint.
func swapBaseURL(t *testing.T, target string) func() {
	t.Helper()
	original := BaseURL
	BaseURL = target
	return func() {
		BaseURL = original
	}
}

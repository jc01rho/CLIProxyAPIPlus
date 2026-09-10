package zcode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestIsOffPeakWindow verifies the KST [00:00, 10:00) boundaries.
func TestIsOffPeakWindow(t *testing.T) {
	cases := []struct {
		utc  string
		want bool
	}{
		{"2026-09-10T14:59:00Z", false}, // KST 23:59 — before midnight open
		{"2026-09-10T15:00:00Z", true},  // KST 00:00 inclusive
		{"2026-09-10T00:59:00Z", true},  // KST 09:59
		{"2026-09-10T01:00:00Z", false}, // KST 10:00 exclusive
		{"2026-09-10T12:00:00Z", false}, // KST 21:00
	}
	for _, tc := range cases {
		ts, err := time.Parse(time.RFC3339, tc.utc)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.utc, err)
		}
		if got := IsOffPeakWindow(ts); got != tc.want {
			t.Errorf("IsOffPeakWindow(%s) = %v, want %v", tc.utc, got, tc.want)
		}
	}
}

// TestIsFlashModelID verifies flash detection is case-insensitive.
func TestIsFlashModelID(t *testing.T) {
	for _, id := range []string{"glm-5.3-Flash", "glm-5.3-flash", "FLASH-1"} {
		if !IsFlashModelID(id) {
			t.Errorf("IsFlashModelID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"glm-5.3", "glm-5.2", "flashing-past"} {
		if id == "flashing-past" {
			continue // contains "flash" — still true by contract
		}
		if IsFlashModelID(id) {
			t.Errorf("IsFlashModelID(%q) = true, want false", id)
		}
	}
}

// TestOffPeakAvailability verifies the {data:{can_take_number}} envelope.
func TestOffPeakAvailability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/availability" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer jwt-1" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Coding-Plan-Api-Key"); got != "id.secret" {
			t.Errorf("X-Coding-Plan-Api-Key = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"can_take_number": true}})
	}))
	defer srv.Close()
	oldBase := OffPeakTicketBaseURL
	OffPeakTicketBaseURL = srv.URL
	defer func() { OffPeakTicketBaseURL = oldBase }()

	if !OffPeakAvailability(context.Background(), "jwt-1", "id.secret") {
		t.Fatal("availability should be true")
	}
}

// TestEnsureOffPeakTicketReadyImmediately verifies the fast path.
func TestEnsureOffPeakTicketReadyImmediately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"ticket_id": "tk-1", "state": "ready",
		}})
	}))
	defer srv.Close()
	oldBase := OffPeakTicketBaseURL
	OffPeakTicketBaseURL = srv.URL
	defer func() { OffPeakTicketBaseURL = oldBase }()

	id, err := EnsureOffPeakTicket(context.Background(), "jwt", "id.secret", "task-1")
	if err != nil {
		t.Fatalf("EnsureOffPeakTicket: %v", err)
	}
	if id != "tk-1" {
		t.Errorf("ticket id = %q", id)
	}
}

// TestEnsureOffPeakTicketQueuedThenReady drives the poll loop with a
// server-dictated cadence.
func TestEnsureOffPeakTicketQueuedThenReady(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ticket":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"ticket_id": "tk-q", "state": "queued", "next_poll_after": 0.002,
			}})
		case "/ticket/status":
			if polls.Add(1) < 2 {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"tickets": []map[string]any{{"ticket_id": "tk-q", "state": "queued"}},
				}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"tickets": []map[string]any{{"ticket_id": "tk-q", "state": "ready"}},
			}})
		}
	}))
	defer srv.Close()
	oldBase := OffPeakTicketBaseURL
	OffPeakTicketBaseURL = srv.URL + "/ticket"
	defer func() { OffPeakTicketBaseURL = oldBase }()

	id, err := EnsureOffPeakTicket(context.Background(), "jwt", "id.secret", "task-1")
	if err != nil {
		t.Fatalf("EnsureOffPeakTicket: %v", err)
	}
	if id != "tk-q" {
		t.Errorf("ticket id = %q", id)
	}
}

// TestEnsureOffPeakTicketTakeLimit verifies the 3103-style rejection surfaces
// as an error (caller falls back to ultra).
func TestEnsureOffPeakTicketTakeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 3103, "msg": "free tier limit reached"})
	}))
	defer srv.Close()
	oldBase := OffPeakTicketBaseURL
	OffPeakTicketBaseURL = srv.URL
	defer func() { OffPeakTicketBaseURL = oldBase }()

	if _, err := EnsureOffPeakTicket(context.Background(), "jwt", "id.secret", "task-1"); err == nil {
		t.Fatal("take limit must return an error")
	}
}

// TestOffPeakTicketFresh verifies the reuse conditions.
func TestOffPeakTicketFresh(t *testing.T) {
	now := time.Date(2026, 9, 10, 15, 30, 0, 0, time.UTC) // KST 00:30 — in window
	ticket := &OffPeakTicketState{JWT: "jwt", APIKey: "key", TicketID: "tk", TakenAt: now, WindowEpoch: kstWindowEpoch(now)}
	if !OffPeakTicketFresh(ticket, now.Add(5*time.Minute), "jwt", "key") {
		t.Error("fresh ticket inside window should be reusable")
	}
	if OffPeakTicketFresh(ticket, now.Add(11*time.Minute), "jwt", "key") {
		t.Error("ticket older than 10 minutes must not be reused")
	}
	if OffPeakTicketFresh(ticket, now, "other-jwt", "key") {
		t.Error("credential mismatch must not be reused")
	}
	// Out of window: KST 11:00.
	later := now.Add(10 * time.Hour)
	if OffPeakTicketFresh(ticket, later, "jwt", "key") {
		t.Error("ticket must not be reused outside the campaign window")
	}
}

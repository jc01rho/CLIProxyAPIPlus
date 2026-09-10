// Package zcode — off-peak ("Idle plan") ticket gateway client.
//
// Reverse-engineered from the ZCode 3.11.2 host bundle and mirrored from the
// omo-zcode-oauth extension reference (extensions/glm-zcode/offpeak.ts).
// During the GLM-5.3-Flash usage campaign window (23:00-09:00 SGT ==
// 00:00-10:00 KST daily) ZCode routes flash traffic through
// `zcode.z.ai/api/v1/off-peak/anthropic` authenticated by the zcode JWT
// (broker `data.token`) plus a queue ticket from `/api/v1/off-peak/ticket`,
// which the plan bills at zero quota. Ticket protocol (mirrored by the app's
// e2e mock):
//
//	GET  /ticket/availability            -> {data:{can_take_number, next_take_at?}}
//	POST /ticket          {task_id}      -> {ticket_id, state: queued|ready|active, next_poll_after, ...}
//	POST /ticket/status   {ticket_ids[]} -> {data:{tickets:[{ticket_id, state, position, ...}]}}
//	POST /ticket/{id}/settle             -> {state:"settled"}
//
// Inference requests carry Authorization: Bearer <jwt>, X-Coding-Plan-Api-Key,
// and X-Off-Peak-Ticket-ID; error codes 3102 (wrong ticket), 3103 (take
// limit), 3105 (not ready). Every failure degrades to the normal signed ultra
// path at the executor layer.
package zcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OffPeakBaseURL is the off-peak ("Idle plan") inference gateway.
const OffPeakBaseURL = "https://zcode.z.ai/api/v1/off-peak/anthropic"

// OffPeakTicketBaseURL is the ticket queue root. Var (not const) so tests can
// point it at a mock server.
var OffPeakTicketBaseURL = "https://zcode.z.ai/api/v1/off-peak/ticket"

const (
	// offPeakRequestTimeout bounds each ticket HTTP call.
	offPeakRequestTimeout = 15 * time.Second
	// OffPeakReadyWaitMS is how long EnsureOffPeakTicket waits for the queue.
	OffPeakReadyWaitMS = 120 * time.Second
	// OffPeakMinPollIntervalMS / OffPeakMaxPollIntervalMS clamp the
	// server-dictated poll cadence (next_poll_after seconds).
	OffPeakMinPollIntervalMS = 2 * time.Second
	OffPeakMaxPollIntervalMS = 30 * time.Second
	offPeakDefaultPollMs     = 10 * time.Second
	// OffPeakTicketFreshMS bounds reuse of a cached ticket.
	OffPeakTicketFreshMS = 10 * time.Minute
	// offPeakPoll attempts in the client loop.
	offPeakMaxPolls = 256
)

// IsOffPeakWindow reports whether now falls in the campaign window, KST
// [00:00, 10:00), i.e. UTC [15:00, 25:00) of the previous calendar day.
// Fixed UTC+9 offset — the campaign definition is location-independent.
func IsOffPeakWindow(now time.Time) bool {
	kstHour := (now.UTC().Hour() + 9) % 24
	return kstHour < 10
}

// IsFlashModelID reports whether the model id targets a flash model (the only
// class the campaign routes through the off-peak gateway).
func IsFlashModelID(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "flash")
}

// kstWindowEpoch is the KST calendar-day bucket. Tickets never outlive their
// campaign window.
func kstWindowEpoch(now time.Time) int64 {
	return now.Add(-9*time.Hour).UTC().Unix() / 86_400
}

// offPeakTicketHeaders are the auth headers every ticket call carries: the
// ZCode source identity, the zcode JWT, and the provisioned API key.
func offPeakTicketHeaders(jwt, apiKey string) http.Header {
	h := http.Header{}
	h.Set("User-Agent", "ZCode/3.11.2")
	h.Set("HTTP-Referer", "https://zcode.z.ai")
	h.Set("X-Title", "Z Code@electron")
	h.Set("X-ZCode-Agent", "glm")
	h.Set("X-ZCode-App-Version", "3.11.2")
	h.Set("X-Release-Channel", "production")
	h.Set("Authorization", "Bearer "+jwt)
	h.Set("X-Coding-Plan-Api-Key", apiKey)
	return h
}

// OffPeakAvailability reports whether the plan currently grants an off-peak
// ticket. Any failure reads as false.
func OffPeakAvailability(ctx context.Context, jwt, apiKey string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OffPeakTicketBaseURL+"/availability", nil)
	if err != nil {
		return false
	}
	req.Header = offPeakTicketHeaders(jwt, apiKey)
	client := &http.Client{Timeout: offPeakRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var envelope struct {
		Code *int `json:"code"`
		Data *struct {
			CanTakeNumber bool `json:"can_take_number"`
		} `json:"data"`
		CanTakeNumber bool `json:"can_take_number"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&envelope); err != nil {
		return false
	}
	// Both shapes observed: {data:{can_take_number}} and a flat body.
	if envelope.Data != nil {
		return envelope.Data.CanTakeNumber
	}
	return envelope.CanTakeNumber
}

// clampOffPeakPollInterval clamps the server's next_poll_after seconds to a
// sane poll cadence.
func clampOffPeakPollInterval(seconds *float64) time.Duration {
	if seconds == nil || *seconds < 0 {
		return offPeakDefaultPollMs
	}
	d := time.Duration(*seconds * float64(time.Second))
	if d < OffPeakMinPollIntervalMS {
		return OffPeakMinPollIntervalMS
	}
	if d > OffPeakMaxPollIntervalMS {
		return OffPeakMaxPollIntervalMS
	}
	return d
}

// OffPeakTicket is a ticket entry from the take/status responses.
type OffPeakTicket struct {
	TicketID    string   `json:"ticket_id"`
	State       string   `json:"state"`
	NextPollSec *float64 `json:"next_poll_after"`
	Position    *int     `json:"position"`
}

// EnsureOffPeakTicket takes a ticket for taskID and waits for it to become
// ready. Returns the ticket id, or an error when unavailable (caller keeps
// the ultra path).
func EnsureOffPeakTicket(ctx context.Context, jwt, apiKey, taskID string) (string, error) {
	body, _ := json.Marshal(map[string]string{"task_id": taskID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OffPeakTicketBaseURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("zcode off-peak: build take request: %w", err)
	}
	req.Header = offPeakTicketHeaders(jwt, apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: offPeakRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("zcode off-peak: take request: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("zcode off-peak: read take response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("zcode off-peak: take rejected: %d %s", resp.StatusCode, redactSecrets(string(raw)))
	}
	var envelope struct {
		Code *int           `json:"code"`
		Data *OffPeakTicket `json:"data"`
		OffPeakTicket
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("zcode off-peak: decode take response: %w", err)
	}
	ticket := envelope.OffPeakTicket
	if envelope.Data != nil {
		ticket = *envelope.Data
	}
	if ticket.TicketID == "" {
		return "", fmt.Errorf("zcode off-peak: take response missing ticket_id")
	}

	deadline := time.Now().Add(OffPeakReadyWaitMS)
	state := ticket.State
	pollInterval := clampOffPeakPollInterval(ticket.NextPollSec)
	for polls := 0; polls < offPeakMaxPolls; polls++ {
		if state == "ready" || state == "active" {
			return ticket.TicketID, nil
		}
		if state != "queued" {
			return "", fmt.Errorf("zcode off-peak: ticket state %q", state)
		}
		if time.Now().Add(pollInterval).After(deadline) {
			return "", fmt.Errorf("zcode off-peak: queue wait exceeded %v", OffPeakReadyWaitMS)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
		statusBody, _ := json.Marshal(map[string][]string{"ticket_ids": {ticket.TicketID}})
		statusReq, err := http.NewRequestWithContext(ctx, http.MethodPost, OffPeakTicketBaseURL+"/status", bytes.NewReader(statusBody))
		if err != nil {
			return "", fmt.Errorf("zcode off-peak: build status request: %w", err)
		}
		statusReq.Header = offPeakTicketHeaders(jwt, apiKey)
		statusReq.Header.Set("Content-Type", "application/json")
		statusResp, err := client.Do(statusReq)
		if err != nil {
			return "", fmt.Errorf("zcode off-peak: status request: %w", err)
		}
		statusRaw, err := io.ReadAll(io.LimitReader(statusResp.Body, 64*1024))
		_ = statusResp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("zcode off-peak: read status response: %w", err)
		}
		if statusResp.StatusCode < 200 || statusResp.StatusCode >= 300 {
			return "", fmt.Errorf("zcode off-peak: status rejected: %d %s", statusResp.StatusCode, redactSecrets(string(statusRaw)))
		}
		var statusEnvelope struct {
			Data struct {
				Tickets []OffPeakTicket `json:"tickets"`
			} `json:"data"`
			Tickets []OffPeakTicket `json:"tickets"`
		}
		if err := json.Unmarshal(statusRaw, &statusEnvelope); err != nil {
			return "", fmt.Errorf("zcode off-peak: decode status response: %w", err)
		}
		tickets := statusEnvelope.Data.Tickets
		if len(tickets) == 0 {
			tickets = statusEnvelope.Tickets
		}
		var entry *OffPeakTicket
		for i := range tickets {
			if tickets[i].TicketID == ticket.TicketID {
				entry = &tickets[i]
				break
			}
		}
		if entry == nil {
			return "", fmt.Errorf("zcode off-peak: status response missing ticket %s", ticket.TicketID)
		}
		if entry.State == "" {
			entry.State = "queued"
		}
		state = entry.State
		next := entry.NextPollSec
		if next == nil && ticket.NextPollSec != nil {
			next = ticket.NextPollSec
		}
		pollInterval = clampOffPeakPollInterval(next)
	}
	return "", fmt.Errorf("zcode off-peak: poll limit reached")
}

// OffPeakTicketState is a cached reusable ticket bound to one credential pair.
type OffPeakTicketState struct {
	JWT         string
	APIKey      string
	TicketID    string
	TakenAt     time.Time
	WindowEpoch int64
}

// OffPeakTicketFresh reports whether the cached ticket is reusable: same
// credential pair, same KST window, still fresh, still in the window.
func OffPeakTicketFresh(ticket *OffPeakTicketState, now time.Time, jwt, apiKey string) bool {
	return ticket != nil &&
		ticket.JWT == jwt &&
		ticket.APIKey == apiKey &&
		ticket.WindowEpoch == kstWindowEpoch(now) &&
		now.Sub(ticket.TakenAt) < OffPeakTicketFreshMS &&
		IsOffPeakWindow(now)
}

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const (
	antigravityGeminiQuota429Body = "{\"error\":{\"code\":429,\"status\":\"RESOURCE_EXHAUSTED\",\"message\":\"You have exhausted your capacity on this model. Your quota will reset after 1h43m56s.\"}}"
	antigravityClaudeQuota429Body = "{\"error\":{\"code\":429,\"status\":\"RESOURCE_EXHAUSTED\",\"type\":\"rate_limit_error\",\"message\":\"Rate limit exceeded\"}}"
)

type antigravityUpstreamStatusError struct {
	code int
	body string
}

func (e antigravityUpstreamStatusError) Error() string   { return e.body }
func (e antigravityUpstreamStatusError) StatusCode() int { return e.code }

func newAntigravityHandoffManager(t *testing.T, handoffEnabled bool) *Manager {
	t.Helper()
	ctx := WithSkipPersist(context.Background())
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetConfig(&internalconfig.Config{AntigravityPrimaryHandoff: handoffEnabled})
	for _, auth := range []*Auth{
		{ID: "ag-handoff-primary", Provider: "antigravity", Status: StatusActive, PrimaryInfo: &PrimaryInfo{IsPrimary: true, Order: 1}},
		{ID: "ag-handoff-standby", Provider: "antigravity", Status: StatusDisabled, Disabled: true, PrimaryInfo: &PrimaryInfo{IsPrimary: false, Order: 2}},
	} {
		if _, errRegister := m.Register(ctx, auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	return m
}

func markResultBounded(t *testing.T, m *Manager, result Result) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.MarkResult(context.Background(), result)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("MarkResult did not return within 10s: the antigravity handoff path re-acquires m.mu while MarkResult holds it")
	}
}

func assertAntigravityHandoff(t *testing.T, m *Manager, wantPromoted bool) {
	t.Helper()
	oldPrimary, ok := m.GetByID("ag-handoff-primary")
	if !ok || oldPrimary == nil || oldPrimary.PrimaryInfo == nil {
		t.Fatal("former primary missing from manager")
	}
	promoted, ok := m.GetByID("ag-handoff-standby")
	if !ok || promoted == nil || promoted.PrimaryInfo == nil {
		t.Fatal("standby missing from manager")
	}
	promotedNow := oldPrimary.Disabled && !oldPrimary.PrimaryInfo.IsPrimary &&
		!promoted.Disabled && promoted.PrimaryInfo.IsPrimary
	if promotedNow != wantPromoted {
		t.Fatalf("handoff promoted = %v, want %v (old=%+v new=%+v)", promotedNow, wantPromoted, oldPrimary, promoted)
	}
	if !wantPromoted {
		if !promoted.Disabled || promoted.PrimaryInfo.IsPrimary {
			t.Fatalf("standby state changed without handoff: %+v", promoted)
		}
		return
	}
	available, errSel := m.availableAuthsForRouteModel(m.List(), "antigravity", "gemini-3-pro", time.Now())
	if errSel != nil {
		t.Fatalf("availableAuthsForRouteModel() error = %v", errSel)
	}
	if len(available) != 1 || available[0].ID != "ag-handoff-standby" {
		ids := make([]string, 0, len(available))
		for _, auth := range available {
			ids = append(ids, auth.ID)
		}
		t.Fatalf("post-handoff selection = %v, want [ag-handoff-standby]", ids)
	}
}

func TestAntigravityHandoffPromotesStandbyOnQuotaExhaustion(t *testing.T) {
	bodies := []struct {
		name string
		body string
	}{
		{"gemini", antigravityGeminiQuota429Body},
		{"claude", antigravityClaudeQuota429Body},
	}
	for _, model := range []string{"gemini-3-pro", "gemini-3-flash", "claude-sonnet-4-5"} {
		for _, tc := range bodies {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				m := newAntigravityHandoffManager(t, true)
				markResultBounded(t, m, Result{
					AuthID:          "ag-handoff-primary",
					Provider:        "antigravity",
					Model:           model,
					Success:         false,
					CredentialScope: true,
					Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: tc.body},
				})
				assertAntigravityHandoff(t, m, true)
			})
		}
	}
}

func TestAntigravityHandoffAppliesExecutorStatusError(t *testing.T) {
	m := newAntigravityHandoffManager(t, true)
	execErr := antigravityUpstreamStatusError{code: http.StatusTooManyRequests, body: antigravityGeminiQuota429Body}
	result := Result{
		AuthID:          "ag-handoff-primary",
		Provider:        "antigravity",
		Model:           "gemini-3-pro",
		Success:         false,
		CredentialScope: true,
		Error:           resultErrorFromError(execErr),
	}
	if result.Error == nil || result.Error.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("converted error = %+v, want HTTP 429", result.Error)
	}
	markResultBounded(t, m, result)
	assertAntigravityHandoff(t, m, true)
}

func TestAntigravityHandoffDisabledByConfig(t *testing.T) {
	m := newAntigravityHandoffManager(t, false)
	markResultBounded(t, m, Result{
		AuthID:          "ag-handoff-primary",
		Provider:        "antigravity",
		Model:           "gemini-3-pro",
		Success:         false,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: antigravityGeminiQuota429Body},
	})
	assertAntigravityHandoff(t, m, false)
}

func TestAntigravityHandoffIgnoresOtherStatusCodes(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		m := newAntigravityHandoffManager(t, true)
		markResultBounded(t, m, Result{
			AuthID:          "ag-handoff-primary",
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: true,
			Error:           &Error{HTTPStatus: status, Message: "upstream error"},
		})
		assertAntigravityHandoff(t, m, false)
	}
}

// newAntigravityHandoffChain registers len(orders) managed credentials ag-chain-<order>;
// the one whose Order equals primaryOrder is the enabled primary, the rest are standbys.
func newAntigravityHandoffChain(t *testing.T, primaryOrder int, orders ...int) *Manager {
	t.Helper()
	ctx := WithSkipPersist(context.Background())
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetConfig(&internalconfig.Config{AntigravityPrimaryHandoff: true})
	for _, order := range orders {
		isPrimary := order == primaryOrder
		auth := &Auth{
			ID:          chainAuthID(order),
			Provider:    "antigravity",
			Status:      StatusDisabled,
			Disabled:    true,
			PrimaryInfo: &PrimaryInfo{IsPrimary: false, Order: order},
		}
		if isPrimary {
			auth.Status = StatusActive
			auth.Disabled = false
			auth.PrimaryInfo.IsPrimary = true
		}
		if _, errRegister := m.Register(ctx, auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	return m
}

func chainAuthID(order int) string { return "ag-chain-" + string(rune('0'+order)) }

func failChainPrimary(t *testing.T, m *Manager, order int, status int) {
	t.Helper()
	markResultBounded(t, m, Result{
		AuthID:          chainAuthID(order),
		Provider:        "antigravity",
		Model:           "gemini-3-flash",
		Success:         false,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: status, Message: antigravityGeminiQuota429Body},
	})
}

func assertChainPrimary(t *testing.T, m *Manager, wantOrder int) {
	t.Helper()
	var primaries []string
	for _, auth := range m.List() {
		if auth.PrimaryInfo != nil && auth.PrimaryInfo.IsPrimary && !auth.Disabled {
			primaries = append(primaries, auth.ID)
		}
	}
	if len(primaries) != 1 || primaries[0] != chainAuthID(wantOrder) {
		t.Fatalf("enabled primaries = %v, want only %s", primaries, chainAuthID(wantOrder))
	}
}

// The last credential in the chain used to be a dead end: there was no "next", so a
// 403/429 on it disabled nothing and every request kept failing on it.
func TestAntigravityHandoffWrapsAroundFromLastOrder(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		m := newAntigravityHandoffChain(t, 3, 1, 2, 3)
		failChainPrimary(t, m, 3, status)
		assertChainPrimary(t, m, 1)
		old, _ := m.GetByID(chainAuthID(3))
		if !old.Disabled || old.PrimaryInfo.IsPrimary {
			t.Fatalf("status %d: former primary not demoted: %+v", status, old)
		}
	}
}

// A credential that was just demoted by a failure must not be handed the traffic back
// by the next failure; with no usable standby the failing primary stays enabled.
func TestAntigravityHandoffDoesNotPingPongBetweenFailedCredentials(t *testing.T) {
	m := newAntigravityHandoffChain(t, 1, 1, 2)
	failChainPrimary(t, m, 1, http.StatusTooManyRequests)
	assertChainPrimary(t, m, 2)
	failChainPrimary(t, m, 2, http.StatusTooManyRequests)
	assertChainPrimary(t, m, 2)
	first, _ := m.GetByID(chainAuthID(1))
	if !first.Disabled || first.PrimaryInfo.IsPrimary {
		t.Fatalf("credential demoted a moment ago was promoted again: %+v", first)
	}
}

func TestAntigravityHandoffSkipsBlockedStandbyAndUsesNextOne(t *testing.T) {
	m := newAntigravityHandoffChain(t, 1, 1, 2, 3)
	failChainPrimary(t, m, 1, http.StatusTooManyRequests) // 1 -> 2, 1 blocked
	assertChainPrimary(t, m, 2)
	failChainPrimary(t, m, 2, http.StatusForbidden) // 2 -> 3, not back to 1
	assertChainPrimary(t, m, 3)
	failChainPrimary(t, m, 3, http.StatusTooManyRequests) // 3 -> wrap, 1 and 2 blocked
	assertChainPrimary(t, m, 3)
}

func TestAntigravityHandoffBlockExpires(t *testing.T) {
	m := newAntigravityHandoffChain(t, 1, 1, 2)
	failChainPrimary(t, m, 1, http.StatusTooManyRequests)
	assertChainPrimary(t, m, 2)

	m.mu.Lock()
	m.auths[chainAuthID(1)].handoffBlockedUntil = time.Now().Add(-time.Second)
	m.mu.Unlock()

	failChainPrimary(t, m, 2, http.StatusTooManyRequests)
	assertChainPrimary(t, m, 1)
}

// A restart (or an auth-file reload) drops the in-memory block, so the credential is
// eligible again instead of staying parked forever.
func TestAntigravityHandoffBlockIsRuntimeOnly(t *testing.T) {
	m := newAntigravityHandoffChain(t, 1, 1, 2)
	failChainPrimary(t, m, 1, http.StatusTooManyRequests)
	demoted, _ := m.GetByID(chainAuthID(1))
	if !demoted.handoffBlockedUntil.After(time.Now()) {
		t.Fatalf("demoted credential has no block deadline: %+v", demoted.handoffBlockedUntil)
	}
	if encoded, err := json.Marshal(demoted); err != nil || strings.Contains(string(encoded), "handoff") {
		t.Fatalf("block leaked into serialized auth: %s (err=%v)", encoded, err)
	}
}

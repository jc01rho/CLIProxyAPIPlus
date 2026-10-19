package auth

import (
	"context"
	"net/http"
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

func TestAntigravityHandoffIgnoresQuotaExhaustion(t *testing.T) {
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
				assertAntigravityHandoff(t, m, false)
			})
		}
	}
}

func TestAntigravityHandoffPromotesStandbyOnGatewayFailure(t *testing.T) {
	m := newAntigravityHandoffManager(t, true)
	markResultBounded(t, m, Result{
		AuthID:          "ag-handoff-primary",
		Provider:        "antigravity",
		Model:           "gemini-3-pro",
		Success:         false,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "service unavailable"},
	})
	assertAntigravityHandoff(t, m, true)
}

func TestAntigravityHandoffSkipsGPTAndClaudeRoutes(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		for _, model := range []string{"claude-sonnet-4-5", "gpt-oss-120b-medium"} {
			m := newAntigravityHandoffManager(t, true)
			markResultBounded(t, m, Result{
				AuthID:          "ag-handoff-primary",
				Provider:        "antigravity",
				Model:           model,
				Success:         false,
				CredentialScope: true,
				Error:           &Error{HTTPStatus: status, Message: "upstream error"},
			})
			assertAntigravityHandoff(t, m, false)
		}
	}
}

func TestAntigravityHandoffAppliesExecutorStatusError(t *testing.T) {
	m := newAntigravityHandoffManager(t, true)
	execErr := antigravityUpstreamStatusError{code: http.StatusServiceUnavailable, body: "service unavailable"}
	result := Result{
		AuthID:          "ag-handoff-primary",
		Provider:        "antigravity",
		Model:           "gemini-3-pro",
		Success:         false,
		CredentialScope: true,
		Error:           resultErrorFromError(execErr),
	}
	if result.Error == nil || result.Error.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("converted error = %+v, want HTTP 503", result.Error)
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

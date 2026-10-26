package auth

import (
	"net/http"
	"testing"
	"time"
)

// A refusal produced by the proxy itself (a local rate limit) is request-scoped: it says
// nothing about the credential, so it must neither cool it down nor trigger a handoff.
type antigravityLocalRefusal struct{}

func (antigravityLocalRefusal) Error() string         { return "antigravity local rate limit exceeded" }
func (antigravityLocalRefusal) StatusCode() int       { return http.StatusTooManyRequests }
func (antigravityLocalRefusal) IsRequestScoped() bool { return true }

func TestAntigravityHandoffIgnoresRequestScopedLocalRefusal(t *testing.T) {
	m := newAntigravityHandoffManager(t, true)
	result := Result{
		AuthID:          "ag-handoff-primary",
		Provider:        "antigravity",
		Model:           "gemini-3-pro",
		Success:         false,
		CredentialScope: true,
		Error:           resultErrorFromError(antigravityLocalRefusal{}),
	}
	if result.Error == nil || result.Error.HTTPStatus != http.StatusTooManyRequests || !result.Error.IsRequestScoped() {
		t.Fatalf("converted error = %+v, want request-scoped HTTP 429", result.Error)
	}
	markResultBounded(t, m, result)
	assertAntigravityHandoff(t, m, false)

	primary, _ := m.GetByID("ag-handoff-primary")
	if primary.Unavailable || !primary.NextRetryAfter.IsZero() {
		t.Fatalf("local refusal put the credential on cooldown: unavailable=%v retryAfter=%v", primary.Unavailable, primary.NextRetryAfter)
	}
	for model, state := range primary.ModelStates {
		if state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
			t.Fatalf("local refusal put model %s on cooldown: %+v", model, state)
		}
	}
	available, errSel := m.availableAuthsForRouteModel(m.List(), "antigravity", "gemini-3-pro", time.Now())
	if errSel != nil || len(available) != 1 || available[0].ID != "ag-handoff-primary" {
		t.Fatalf("primary not selectable after a local refusal: %v (err=%v)", available, errSel)
	}
}

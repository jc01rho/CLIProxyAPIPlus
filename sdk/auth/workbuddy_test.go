package auth

import (
	"testing"
	"time"
)

func TestWorkBuddyAuthenticatorIdentityAndRefreshLead(t *testing.T) {
	authenticator := NewWorkBuddyAuthenticator()
	if got := authenticator.Provider(); got != "workbuddy" {
		t.Fatalf("Provider() = %q, want workbuddy", got)
	}
	lead := authenticator.RefreshLead()
	if lead == nil || *lead != 24*time.Hour {
		t.Fatalf("RefreshLead() = %v, want 24h", lead)
	}
}

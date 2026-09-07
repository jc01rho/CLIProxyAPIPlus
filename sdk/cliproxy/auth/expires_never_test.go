package auth

import (
	"testing"
	"time"
)

func TestExpiresNeverAccessor(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth *Auth
		want bool
	}{
		{name: "nil auth"},
		{name: "absent", auth: &Auth{}},
		{name: "metadata canonical true", auth: &Auth{Metadata: map[string]any{"expires_never": true}}, want: true},
		{name: "metadata legacy true", auth: &Auth{Metadata: map[string]any{"expires-never": true}}, want: true},
		{name: "metadata string true", auth: &Auth{Metadata: map[string]any{"expires_never": "true"}}, want: true},
		{name: "metadata explicit false", auth: &Auth{Metadata: map[string]any{"expires_never": false}}},
		{name: "attribute canonical", auth: &Auth{Attributes: map[string]string{"expires_never": "true"}}, want: true},
		{name: "attribute legacy", auth: &Auth{Attributes: map[string]string{"expires-never": "true"}}, want: true},
		{name: "attribute false", auth: &Auth{Attributes: map[string]string{"expires_never": "false"}}},
		{name: "attribute garbage", auth: &Auth{Attributes: map[string]string{"expires_never": "sometimes"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.auth.ExpiresNever(); got != tc.want {
				t.Fatalf("ExpiresNever() = %t, want %t", got, tc.want)
			}
		})
	}
}

// The motivating scenario: a static third-party claude key (custom base_url, no
// refresh token) whose file carries a past "expired" timestamp used to be blocked
// permanently. With expires-never the timestamp is ignored.
func TestExpiresNeverSuppressesExpiryBlocking(t *testing.T) {
	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	newAuth := func() *Auth {
		return &Auth{
			ID:       "claude-3p",
			Provider: "claude",
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  "https://third-party.example.com/v1",
			},
			Metadata: map[string]any{
				"access_token": "sk-static-non-jwt-token",
				"expired":      past,
			},
		}
	}

	// Without the flag: blocked (regression guard for the original bug).
	without := newAuth()
	if exp, ok := without.AccessTokenExpirationTime(); !ok || !exp.Before(time.Now()) {
		t.Fatalf("precondition: expected parsed past expiry, got %v ok=%t", exp, ok)
	}
	if blocked, _, _ := isAuthBlockedForModel(without, "sajjon", time.Now()); !blocked {
		t.Fatal("precondition: expected the expired timestamp to block without the flag")
	}

	// With the flag: no expiry at all, not blocked.
	with := newAuth()
	with.Metadata["expires_never"] = true
	if exp, ok := with.AccessTokenExpirationTime(); ok {
		t.Fatalf("AccessTokenExpirationTime() = %v ok=true, want no expiry", exp)
	}
	if exp, ok := with.ExpirationTime(); ok {
		t.Fatalf("ExpirationTime() = %v ok=true, want no expiry", exp)
	}
	if blocked, reason, next := isAuthBlockedForModel(with, "sajjon", time.Now()); blocked {
		t.Fatalf("expires-never auth still blocked: reason=%v next=%v", reason, next)
	}
	// JWT tokens are also ignored when the flag is set.
	with.Metadata["access_token"] = "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjEzMDAwMDAwMDB9.sig"
	if _, ok := with.AccessTokenExpirationTime(); ok {
		t.Fatal("JWT exp should be ignored when expires-never is set")
	}
}

func TestCanonicalCredentialMetadataKeyMapsExpiresNever(t *testing.T) {
	if got := CanonicalCredentialMetadataKey("expires-never"); got != "expires_never" {
		t.Fatalf("CanonicalCredentialMetadataKey() = %q, want expires_never", got)
	}
	metadata := map[string]any{"expires-never": true}
	NormalizeCredentialMetadata(metadata)
	if got, ok := metadata["expires_never"]; !ok || got != true {
		t.Fatalf("normalized metadata = %v, want canonical expires_never=true", metadata)
	}
}

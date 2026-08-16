package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
)

func TestResolveAntigravityOAuthCallbackIgnoresCallbackPortOverride(t *testing.T) {
	t.Parallel()

	port, uri := resolveAntigravityOAuthCallback(8085)
	if port != antigravity.CallbackPort {
		t.Fatalf("port = %d, want %d", port, antigravity.CallbackPort)
	}
	if uri != antigravity.RedirectURI {
		t.Fatalf("redirectURI = %q, want %q", uri, antigravity.RedirectURI)
	}
	if uri != "http://localhost:51121/oauth-callback" {
		t.Fatalf("redirectURI = %q, want registered localhost:51121 callback", uri)
	}
}

func TestResolveAntigravityOAuthCallbackKeepsRegisteredPort(t *testing.T) {
	t.Parallel()

	for _, requested := range []int{0, antigravity.CallbackPort, 51121} {
		port, uri := resolveAntigravityOAuthCallback(requested)
		if port != 51121 || uri != "http://localhost:51121/oauth-callback" {
			t.Fatalf("requested %d: port=%d uri=%q", requested, port, uri)
		}
	}
}

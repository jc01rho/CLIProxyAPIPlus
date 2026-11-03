package auth

import (
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAntigravityClaudeOrGPTRouteDetectsLiteralAndAliasedFamilies(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		antigravityModelAliasChannel: {
			{Name: "claude-sonnet-4-5", Alias: "opus"},
			{Name: "gemini-3-flash", Alias: "flash"},
		},
	})

	cases := []struct {
		name  string
		model string
		want  bool
	}{
		{"literal claude", "claude-sonnet-4-5", true},
		{"literal gpt", "gpt-oss-120b-medium", true},
		{"aliased claude", "opus", true},
		{"aliased gemini", "flash", false},
		{"plain gemini", "gemini-3-pro", false},
		{"empty model", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := manager.antigravityClaudeOrGPTRoute(nil, tc.model); got != tc.want {
				t.Fatalf("antigravityClaudeOrGPTRoute(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestAntigravityClaudeOrGPTRouteReadsAuthAttributeAliases(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	auth := &Auth{ID: "ag-attr-alias", Provider: "antigravity"}
	SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{{Name: "claude-opus-4-6", Alias: "big"}})

	if !manager.antigravityClaudeOrGPTRoute(auth, "big") {
		t.Fatal("antigravityClaudeOrGPTRoute(big) = false, want true through the auth attribute alias")
	}
	if manager.antigravityClaudeOrGPTRoute(nil, "big") {
		t.Fatal("antigravityClaudeOrGPTRoute(big) without the auth = true, want false: auth attribute aliases are auth scoped")
	}
}

func TestShouldAttemptAntigravityCreditsFallbackSkipsClaudeAndGPTRoutes(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		antigravityModelAliasChannel: {{Name: "claude-opus-4-6", Alias: "opus"}},
	})
	quotaErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}

	for _, model := range []string{"claude-opus-4-6", "gpt-5.5", "opus"} {
		if shouldAttemptAntigravityCreditsFallback(manager, quotaErr, []string{"antigravity"}, model) {
			t.Fatalf("shouldAttemptAntigravityCreditsFallback(%q) = true, want false for a Claude/GPT route", model)
		}
	}
	if !shouldAttemptAntigravityCreditsFallback(manager, quotaErr, []string{"antigravity"}, "gemini-3-pro") {
		t.Fatal("shouldAttemptAntigravityCreditsFallback(gemini-3-pro) = false, want true: the guard must only cover Claude/GPT routes")
	}
	if shouldAttemptAntigravityCreditsFallback(manager, quotaErr, []string{"antigravity"}, "claude-opus-4-6") {
		t.Fatal("antigravity credits gate reopened for a literal Claude route")
	}
}

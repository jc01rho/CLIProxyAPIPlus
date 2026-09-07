package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Removing per-auth aliases must actually clear them. Both the auth-file
// synthesizer (nil) and the management patch path (empty array) funnel through
// SetOAuthModelAliasesAttribute, so an empty result has to delete the stored
// attribute instead of leaving the previous mapping live.
func TestSetOAuthModelAliasesAttributeClearsStaleValue(t *testing.T) {
	initial := []internalconfig.OAuthModelAlias{
		{Name: "claude-sonnet-5", Alias: "sonnet", Fork: true},
		{Name: "claude-opus-5", Alias: "opus", Fork: true},
	}

	for _, tc := range []struct {
		name    string
		cleared []internalconfig.OAuthModelAlias
	}{
		{name: "nil list (auth file key removed)", cleared: nil},
		{name: "empty list (management patch cleared rows)", cleared: []internalconfig.OAuthModelAlias{}},
		{name: "all rows sanitize away (name==alias)", cleared: []internalconfig.OAuthModelAlias{{Name: "same", Alias: "same"}}},
		{name: "all rows sanitize away (empty name)", cleared: []internalconfig.OAuthModelAlias{{Name: "", Alias: "orphan"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{ID: "claude-acct", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}
			SetOAuthModelAliasesAttribute(auth, initial)
			if auth.Attributes[oauthModelAliasesAttributeKey] == "" {
				t.Fatal("precondition failed: initial aliases were not stored")
			}

			SetOAuthModelAliasesAttribute(auth, tc.cleared)

			if got := auth.Attributes[oauthModelAliasesAttributeKey]; got != "" {
				t.Errorf("alias attribute = %s, want cleared", got)
			}
			if got := OAuthModelAliasesFromAttributes(auth.Attributes); len(got) != 0 {
				t.Errorf("OAuthModelAliasesFromAttributes() = %v, want empty", got)
			}
			m := NewManager(nil, nil, nil)
			if res := m.resolveOAuthModelAliasWithResult(auth, "sonnet"); res.UpstreamModel != "" {
				t.Errorf("cleared alias %q still resolves to %q", "sonnet", res.UpstreamModel)
			}
		})
	}
}

// A non-empty update must still replace the previous value outright.
func TestSetOAuthModelAliasesAttributeReplacesPreviousValue(t *testing.T) {
	auth := &Auth{ID: "claude-acct", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}
	SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{
		{Name: "claude-sonnet-5", Alias: "sonnet", Fork: true},
	})
	SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{
		{Name: "claude-opus-5", Alias: "opus", Fork: true},
	})

	aliases := OAuthModelAliasesFromAttributes(auth.Attributes)
	if len(aliases) != 1 || aliases[0].Alias != "opus" {
		t.Fatalf("aliases = %v, want only opus", aliases)
	}
	m := NewManager(nil, nil, nil)
	if res := m.resolveOAuthModelAliasWithResult(auth, "sonnet"); res.UpstreamModel != "" {
		t.Errorf("replaced alias %q still resolves to %q", "sonnet", res.UpstreamModel)
	}
	if res := m.resolveOAuthModelAliasWithResult(auth, "opus"); res.UpstreamModel != "claude-opus-5" {
		t.Errorf("opus -> %q, want claude-opus-5", res.UpstreamModel)
	}
}

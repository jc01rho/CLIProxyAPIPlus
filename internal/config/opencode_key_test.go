package config

import "testing"

// Keyless OpenCode entries are the anonymous free tier: the OpenCode client
// sends "public" when no account is connected, and the Management Center
// leaves the key field blank for it. Sanitizing must normalize rather than
// drop them, otherwise saving a provider from the UI deletes it.
func TestSanitizeOpenCodeKeys_KeylessEntryBecomesAnonymous(t *testing.T) {
	cfg := &Config{OpenCodeKey: []OpenCodeKey{
		{BaseURL: "https://opencode.ai/zen/v1", Models: []OpenCodeModel{{Name: "big-pickle", Alias: "pickle"}}},
		{APIKey: "oc_sk_real", BaseURL: "https://opencode.ai/zen/go/v1"},
	}}

	cfg.SanitizeOpenCodeKeys()

	if len(cfg.OpenCodeKey) != 2 {
		t.Fatalf("entries = %d, want 2 (a keyless entry must survive)", len(cfg.OpenCodeKey))
	}
	if got := cfg.OpenCodeKey[0].APIKey; got != OpenCodeAnonymousAPIKey {
		t.Fatalf("keyless entry api-key = %q, want %q", got, OpenCodeAnonymousAPIKey)
	}
	if len(cfg.OpenCodeKey[0].Models) != 1 {
		t.Fatalf("models were dropped: %+v", cfg.OpenCodeKey[0].Models)
	}
	if got := cfg.OpenCodeKey[1].APIKey; got != "oc_sk_real" {
		t.Fatalf("real key rewritten to %q", got)
	}
}

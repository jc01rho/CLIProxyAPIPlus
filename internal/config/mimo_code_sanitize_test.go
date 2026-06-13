package config

import "testing"

func TestSanitizeMiMoCodeKeysTrimsHeadersAndFiltersEmptyClientID(t *testing.T) {
	cfg := &Config{
		MiMoCodeKey: []MiMoCodeKey{
			{
				ClientID: "   ",
				BaseURL:  "https://discard.example.com",
			},
			{
				ClientID: " client-1 ",
				BaseURL:  " https://mimo.example.com ",
				ProxyURL: " http://proxy.example.com ",
				Prefix:   " mimo ",
				Headers: map[string]string{
					" X-Test ": " value ",
					" Empty ":  "   ",
				},
			},
		},
	}

	cfg.SanitizeMiMoCodeKeys()

	if got := len(cfg.MiMoCodeKey); got != 1 {
		t.Fatalf("mimo-code keys len = %d, want 1", got)
	}
	entry := cfg.MiMoCodeKey[0]
	if entry.ClientID != "client-1" {
		t.Fatalf("client-id = %q, want %q", entry.ClientID, "client-1")
	}
	if entry.BaseURL != "https://mimo.example.com" {
		t.Fatalf("base-url = %q, want trimmed", entry.BaseURL)
	}
	if entry.ProxyURL != "http://proxy.example.com" {
		t.Fatalf("proxy-url = %q, want trimmed", entry.ProxyURL)
	}
	if entry.Prefix != "mimo" {
		t.Fatalf("prefix = %q, want trimmed", entry.Prefix)
	}
	if got := entry.Headers["X-Test"]; got != "value" {
		t.Fatalf("header X-Test = %q, want %q", got, "value")
	}
	if _, ok := entry.Headers["Empty"]; ok {
		t.Fatalf("empty header was not removed: %#v", entry.Headers)
	}
}

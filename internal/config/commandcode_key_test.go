package config

import "testing"

func TestSanitizeCommandCodeKeys_keeps_default_base_url_entries(t *testing.T) {
	cfg := &Config{
		CommandCodeKey: []CommandCodeKey{
			{
				APIKey:  " user_default ",
				BaseURL: " ",
				Prefix:  " team ",
				Models: []CommandCodeModel{
					{Name: " upstream ", Alias: " alias "},
				},
			},
		},
	}

	cfg.SanitizeCommandCodeKeys()

	if got := len(cfg.CommandCodeKey); got != 1 {
		t.Fatalf("len(CommandCodeKey) = %d, want 1", got)
	}
	entry := cfg.CommandCodeKey[0]
	if entry.APIKey != "user_default" {
		t.Fatalf("APIKey = %q, want %q", entry.APIKey, "user_default")
	}
	if entry.BaseURL != "" {
		t.Fatalf("BaseURL = %q, want empty default", entry.BaseURL)
	}
	if entry.Prefix != "team" {
		t.Fatalf("Prefix = %q, want %q", entry.Prefix, "team")
	}
	if got := entry.Models[0].Name; got != "upstream" {
		t.Fatalf("Models[0].Name = %q, want %q", got, "upstream")
	}
	if got := entry.Models[0].Alias; got != "alias" {
		t.Fatalf("Models[0].Alias = %q, want %q", got, "alias")
	}
}

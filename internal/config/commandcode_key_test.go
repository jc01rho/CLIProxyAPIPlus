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

func TestSanitizeCommandCodeKeysKeepsNestedOnlyProvider(t *testing.T) {
	weight := 3
	cfg := &Config{
		CommandCodeKey: []CommandCodeKey{
			{
				BaseURL: " https://commandcode.example/v1 ",
				APIKeyEntries: []OpenAICompatibilityAPIKey{
					{
						APIKey:   " key-a ",
						ProxyURL: " socks5://proxy-a.example:1080 ",
						Weight:   &weight,
						Comment:  " primary ",
					},
					{APIKey: " "},
					{APIKey: " key-b "},
				},
			},
		},
	}

	cfg.SanitizeCommandCodeKeys()

	if got := len(cfg.CommandCodeKey); got != 1 {
		t.Fatalf("len(CommandCodeKey) = %d, want 1", got)
	}
	entry := cfg.CommandCodeKey[0]
	if entry.APIKey != "" {
		t.Fatalf("APIKey = %q, want empty legacy key", entry.APIKey)
	}
	if got := len(entry.APIKeyEntries); got != 2 {
		t.Fatalf("len(APIKeyEntries) = %d, want 2", got)
	}
	if got := entry.APIKeyEntries[0].APIKey; got != "key-a" {
		t.Fatalf("APIKeyEntries[0].APIKey = %q, want key-a", got)
	}
	if got := entry.APIKeyEntries[0].ProxyURL; got != "socks5://proxy-a.example:1080" {
		t.Fatalf("APIKeyEntries[0].ProxyURL = %q", got)
	}
	if got := entry.APIKeyEntries[0].Comment; got != "primary" {
		t.Fatalf("APIKeyEntries[0].Comment = %q, want primary", got)
	}
	if got := entry.APIKeyEntries[1].APIKey; got != "key-b" {
		t.Fatalf("APIKeyEntries[1].APIKey = %q, want key-b", got)
	}
}

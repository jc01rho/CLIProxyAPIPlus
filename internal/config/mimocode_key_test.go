package config

import (
	"strings"
	"testing"
)

func TestSanitizeMimocodeKeysRejectsEmptyAPIKey(t *testing.T) {
	cfg := &Config{MimocodeKey: []MimocodeKey{{BaseURL: "https://region.example/v1"}}}
	err := cfg.SanitizeMimocodeKeys()
	if err == nil || !strings.Contains(err.Error(), "mimocode-api-key[0].api-key is required") {
		t.Fatalf("SanitizeMimocodeKeys() error = %v, want required api-key error", err)
	}
}

func TestSanitizeMimocodeKeysPreservesExplicitCredentials(t *testing.T) {
	cfg := &Config{MimocodeKey: []MimocodeKey{{
		APIKey:  "  secret  ",
		BaseURL: " https://region.example/v1/ ",
		Models:  []MimocodeModel{{Name: " mimo-v2.5-pro ", Alias: " pro "}},
	}}}
	if err := cfg.SanitizeMimocodeKeys(); err != nil {
		t.Fatalf("SanitizeMimocodeKeys() error = %v", err)
	}
	entry := cfg.MimocodeKey[0]
	if entry.APIKey != "secret" || entry.BaseURL != "https://region.example/v1/" {
		t.Fatalf("sanitized entry = %+v", entry)
	}
	if entry.Models[0].Name != "mimo-v2.5-pro" || entry.Models[0].Alias != "pro" {
		t.Fatalf("sanitized model = %+v", entry.Models[0])
	}
}

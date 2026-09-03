package cmd

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestStoreMetaAPIKeyCreatesProviderWhenAbsent(t *testing.T) {
	cfg := &config.Config{}

	if !storeMetaAPIKey(cfg, "meta-key-1") {
		t.Fatalf("storeMetaAPIKey reported no change when creating a new provider")
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("expected 1 openai-compatibility entry, got %d", len(cfg.OpenAICompatibility))
	}
	entry := cfg.OpenAICompatibility[0]
	if entry.Name != metaProviderName {
		t.Fatalf("expected provider name %q, got %q", metaProviderName, entry.Name)
	}
	if entry.BaseURL != meta.APIBaseURL {
		t.Fatalf("expected base URL %q, got %q", meta.APIBaseURL, entry.BaseURL)
	}
	if len(entry.APIKeyEntries) != 1 || entry.APIKeyEntries[0].APIKey != "meta-key-1" {
		t.Fatalf("expected one API key entry meta-key-1, got %+v", entry.APIKeyEntries)
	}
	if len(entry.Models) != 1 || entry.Models[0].Name != metaModelName {
		t.Fatalf("expected one model %q, got %+v", metaModelName, entry.Models)
	}
	if entry.Models[0].MaxContextLength != 1048576 {
		t.Fatalf("expected max context length 1048576, got %d", entry.Models[0].MaxContextLength)
	}
}

func TestStoreMetaAPIKeyAppendsToExistingProvider(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    metaProviderName,
				BaseURL: meta.APIBaseURL,
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "meta-key-1"},
				},
			},
		},
	}

	if !storeMetaAPIKey(cfg, "meta-key-2") {
		t.Fatalf("storeMetaAPIKey reported no change when appending a new key")
	}
	entry := cfg.OpenAICompatibility[0]
	if len(entry.APIKeyEntries) != 2 {
		t.Fatalf("expected 2 API key entries, got %d", len(entry.APIKeyEntries))
	}
	if entry.APIKeyEntries[0].APIKey != "meta-key-1" || entry.APIKeyEntries[1].APIKey != "meta-key-2" {
		t.Fatalf("unexpected API key entries: %+v", entry.APIKeyEntries)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("expected no extra provider entry, got %d", len(cfg.OpenAICompatibility))
	}
}

func TestStoreMetaAPIKeySkipsDuplicateKey(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    metaProviderName,
				BaseURL: meta.APIBaseURL,
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "meta-key-1"},
				},
			},
		},
	}

	if storeMetaAPIKey(cfg, "meta-key-1") {
		t.Fatalf("storeMetaAPIKey reported a change for an already-present key")
	}
	if len(cfg.OpenAICompatibility[0].APIKeyEntries) != 1 {
		t.Fatalf("expected API key entry count to stay at 1, got %d", len(cfg.OpenAICompatibility[0].APIKeyEntries))
	}
}

func TestStoreMetaAPIKeyMatchesProviderNameCaseInsensitive(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "Meta", BaseURL: meta.APIBaseURL},
		},
	}

	if !storeMetaAPIKey(cfg, "meta-key-1") {
		t.Fatalf("storeMetaAPIKey reported no change for case-insensitive provider match")
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("expected the existing provider to be reused, got %d entries", len(cfg.OpenAICompatibility))
	}
	if len(cfg.OpenAICompatibility[0].APIKeyEntries) != 1 {
		t.Fatalf("expected key appended to the existing provider, got %+v", cfg.OpenAICompatibility[0].APIKeyEntries)
	}
}

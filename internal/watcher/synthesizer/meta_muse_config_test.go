package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestConfigSynthesizer_when_MetaOpenAICompatibilityUsesKeyEntries(t *testing.T) {
	// Given
	synthesizer := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "meta", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "meta-key"}}},
			{Name: "other", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "other-key"}}},
		}},
		Now:         time.Unix(1, 0),
		IDGenerator: NewStableIDGenerator(),
	}

	// When
	auths, err := synthesizer.Synthesize(ctx)

	// Then
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("auth count = %d, want 2", len(auths))
	}
	if got := auths[0].Attributes["runtime_only"]; got != "true" {
		t.Fatalf("Meta runtime_only = %q, want true", got)
	}
	if got := auths[1].Attributes["runtime_only"]; got != "" {
		t.Fatalf("non-Meta runtime_only = %q, want empty", got)
	}
}

func TestConfigSynthesizer_when_MetaOpenAICompatibilityUsesFallback(t *testing.T) {
	// Given
	synthesizer := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "meta"}}},
		Now:         time.Unix(1, 0),
		IDGenerator: NewStableIDGenerator(),
	}

	// When
	auths, err := synthesizer.Synthesize(ctx)

	// Then
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	if got := auths[0].Attributes["runtime_only"]; got != "true" {
		t.Fatalf("Meta fallback runtime_only = %q, want true", got)
	}
}

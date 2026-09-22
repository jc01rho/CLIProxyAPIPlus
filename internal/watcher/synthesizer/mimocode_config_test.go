package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestConfigSynthesizerMimocodeDefaultsAndEmptyKeys(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{MimocodeKey: []config.MimocodeKey{
			{APIKey: "secret", BaseURL: "https://region.example/v1", Headers: map[string]string{"X-Custom": "value"}},
			{APIKey: "   ", BaseURL: "https://ignored.example/v1"},
		}},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "mimocode" || auth.Attributes["api_key"] != "secret" {
		t.Fatalf("auth = %+v", auth)
	}
	if got := auth.Attributes["base_url"]; got != "https://region.example/v1" {
		t.Fatalf("base_url = %q", got)
	}
	if got := auth.Attributes["header:X-Mimo-Source"]; got != "mimocode-cli" {
		t.Fatalf("X-Mimo-Source = %q", got)
	}
	if got := auth.Attributes["header:X-Custom"]; got != "value" {
		t.Fatalf("X-Custom = %q", got)
	}
}

func TestConfigSynthesizerMimocodeUserSourceHeaderWins(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{MimocodeKey: []config.MimocodeKey{{APIKey: "secret", Headers: map[string]string{"x-mimo-source": "custom"}}}},
		Now:    time.Now(), IDGenerator: NewStableIDGenerator(),
	}
	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if got := auths[0].Attributes["header:x-mimo-source"]; got != "custom" {
		t.Fatalf("custom source header = %q", got)
	}
	if _, exists := auths[0].Attributes["header:X-Mimo-Source"]; exists {
		t.Fatal("default source header should not duplicate case-insensitive override")
	}
}

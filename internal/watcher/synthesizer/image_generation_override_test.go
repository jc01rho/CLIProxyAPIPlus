package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestConfigSynthesizerCodexKeyDisableImageGenerationMetadata(t *testing.T) {
	chat := config.DisableImageGenerationChat
	off := config.DisableImageGenerationOff

	cfg := &config.Config{}
	cfg.CodexKey = []config.CodexKey{
		{APIKey: "chat-key", BaseURL: "https://codex.example.com", DisableImageGeneration: &chat},
		{APIKey: "inherit-key", BaseURL: "https://codex.example.com"},
		{APIKey: "explicit-off-key", BaseURL: "https://codex.example.com", DisableImageGeneration: &off},
	}

	synth := &ConfigSynthesizer{}
	auths, err := synth.Synthesize(&SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}

	byKey := make(map[string]map[string]any, len(auths))
	for _, a := range auths {
		if a.Provider != "codex" {
			continue
		}
		byKey[a.Attributes["api_key"]] = a.Metadata
	}

	if got := byKey["chat-key"]["disable_image_generation"]; got != "chat" {
		t.Errorf("chat-key metadata disable_image_generation = %v, want chat", got)
	}
	if got, ok := byKey["inherit-key"]["disable_image_generation"]; ok {
		t.Errorf("inherit-key metadata disable_image_generation = %v, want absent", got)
	}
	// An explicit false must still travel so it can override a global disable.
	if got := byKey["explicit-off-key"]["disable_image_generation"]; got != "false" {
		t.Errorf("explicit-off-key metadata disable_image_generation = %v, want false", got)
	}
}

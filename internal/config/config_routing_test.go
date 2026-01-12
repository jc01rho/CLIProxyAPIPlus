package config

import (
	"testing"
)

func TestRoutingConfig_FallbackChainMaxLength(t *testing.T) {
	cfg := &RoutingConfig{
		FallbackChain: make([]string, 21), // 21 exceeds max of 20
	}
	for i := range cfg.FallbackChain {
		cfg.FallbackChain[i] = "model-" + string(rune('a'+i%26))
	}

	err := ValidateRoutingConfig(cfg)
	if err == nil {
		t.Error("expected error for chain exceeding 20 items, got nil")
	}
}

func TestRoutingConfig_FallbackChainCircularReference(t *testing.T) {
	tests := []struct {
		name           string
		fallbackModels map[string]string
		wantErr        bool
	}{
		{
			name:           "self reference",
			fallbackModels: map[string]string{"a": "a"},
			wantErr:        true,
		},
		{
			name:           "two-step cycle",
			fallbackModels: map[string]string{"a": "b", "b": "a"},
			wantErr:        true,
		},
		{
			name:           "three-step cycle",
			fallbackModels: map[string]string{"a": "b", "b": "c", "c": "a"},
			wantErr:        true,
		},
		{
			name:           "valid chain no cycle",
			fallbackModels: map[string]string{"a": "b", "b": "c", "c": "d"},
			wantErr:        false,
		},
		{
			name:           "empty map",
			fallbackModels: nil,
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RoutingConfig{
				FallbackModels: tt.fallbackModels,
			}
			err := ValidateRoutingConfig(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRoutingConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRoutingConfig_ValidConfig(t *testing.T) {
	cfg := &RoutingConfig{
		Strategy:       "round-robin",
		Mode:           "provider-based",
		FallbackModels: map[string]string{"gemini-2.5-pro": "gemini-2.0-flash"},
		FallbackChain:  []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash"},
		ProviderPriority: map[string][]string{
			"gemini-2.5-pro": {"gemini-vertex", "gemini-cli"},
		},
		ProviderOrder: []string{"gemini-cli", "gemini-vertex", "gemini-api"},
	}

	err := ValidateRoutingConfig(cfg)
	if err != nil {
		t.Errorf("ValidateRoutingConfig() unexpected error = %v", err)
	}
}

func TestRoutingConfig_NilConfig(t *testing.T) {
	err := ValidateRoutingConfig(nil)
	if err != nil {
		t.Errorf("ValidateRoutingConfig(nil) expected nil, got %v", err)
	}
}

func TestRoutingConfig_ValidChainLength20(t *testing.T) {
	// Exactly 20 items should be valid
	cfg := &RoutingConfig{
		FallbackChain: make([]string, 20),
	}
	for i := range cfg.FallbackChain {
		cfg.FallbackChain[i] = "model-" + string(rune('a'+i%26))
	}

	err := ValidateRoutingConfig(cfg)
	if err != nil {
		t.Errorf("ValidateRoutingConfig() with 20 items should pass, got error = %v", err)
	}
}

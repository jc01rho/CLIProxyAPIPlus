package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRoutingConfigModeParsing(t *testing.T) {
	yamlData := `
routing:
  mode: key-based
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if cfg.Routing.Mode != "key-based" {
		t.Errorf("expected 'key-based', got %q", cfg.Routing.Mode)
	}
}

func TestRoutingConfigModeEmpty(t *testing.T) {
	yamlData := `
routing:
  strategy: round-robin
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if cfg.Routing.Mode != "" {
		t.Errorf("expected empty string, got %q", cfg.Routing.Mode)
	}
}

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

func TestRoutingTokenThresholdRulesParsing(t *testing.T) {
	yamlData := `
routing:
  token-threshold-rules:
    - model-pattern: "gpt-*"
      max-tokens: 100
      billing-class: metered
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(cfg.Routing.TokenThresholdRules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(cfg.Routing.TokenThresholdRules))
	}
	rule := cfg.Routing.TokenThresholdRules[0]
	if rule.ModelPattern != "gpt-*" {
		t.Fatalf("expected model pattern gpt-*, got %q", rule.ModelPattern)
	}
	if rule.MaxTokens != 100 {
		t.Fatalf("expected max tokens 100, got %d", rule.MaxTokens)
	}
	if rule.BillingClass != BillingClassMetered {
		t.Fatalf("expected billing class %q, got %q", BillingClassMetered, rule.BillingClass)
	}
}

func TestSanitizeTokenThresholdRulesDropsInvalidEntries(t *testing.T) {
	cfg := &Config{
		Routing: RoutingConfig{
			TokenThresholdRules: []TokenThresholdRule{
				{ModelPattern: " ", MaxTokens: 0, BillingClass: BillingClassMetered},
				{ModelPattern: "gpt-*", MaxTokens: 10, BillingClass: BillingClassPerRequest},
			},
		},
	}
	cfg.SanitizeTokenThresholdRules()
	if len(cfg.Routing.TokenThresholdRules) != 1 {
		t.Fatalf("expected 1 sanitized rule, got %d", len(cfg.Routing.TokenThresholdRules))
	}
	if !cfg.Routing.TokenThresholdRules[0].Enabled {
		t.Fatal("expected sanitized rule to be enabled")
	}
}

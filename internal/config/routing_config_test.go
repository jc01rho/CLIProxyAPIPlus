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

func TestRoutingTokenThresholdRulesWithMinTokensParsing(t *testing.T) {
	yamlData := `
routing:
  token-threshold-rules:
  - model-pattern: "opus-*"
    min-tokens: 0
    max-tokens: 1500
    billing-class: metered
  - model-pattern: "opus-*"
    min-tokens: 1501
    max-tokens: 2000
    billing-class: per-request
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(cfg.Routing.TokenThresholdRules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(cfg.Routing.TokenThresholdRules))
	}
	rule1 := cfg.Routing.TokenThresholdRules[0]
	if rule1.MinTokens != 0 {
		t.Fatalf("expected min-tokens 0, got %d", rule1.MinTokens)
	}
	if rule1.MaxTokens != 1500 {
		t.Fatalf("expected max-tokens 1500, got %d", rule1.MaxTokens)
	}
	rule2 := cfg.Routing.TokenThresholdRules[1]
	if rule2.MinTokens != 1501 {
		t.Fatalf("expected min-tokens 1501, got %d", rule2.MinTokens)
	}
}

func TestSanitizeTokenThresholdRulesDropsInvalidMinTokens(t *testing.T) {
	cfg := &Config{
		Routing: RoutingConfig{
			TokenThresholdRules: []TokenThresholdRule{
				{ModelPattern: "valid-*", MinTokens: 0, MaxTokens: 100, BillingClass: BillingClassMetered},
				{ModelPattern: "negative-*", MinTokens: -5, MaxTokens: 100, BillingClass: BillingClassMetered},
				{ModelPattern: "invalid-range-*", MinTokens: 200, MaxTokens: 100, BillingClass: BillingClassMetered},
			},
		},
	}
	cfg.SanitizeTokenThresholdRules()
	if len(cfg.Routing.TokenThresholdRules) != 2 {
		t.Fatalf("expected 2 sanitized rules, got %d", len(cfg.Routing.TokenThresholdRules))
	}
	foundNegative := false
	for _, r := range cfg.Routing.TokenThresholdRules {
		if r.ModelPattern == "negative-*" {
			foundNegative = true
			if r.MinTokens != 0 {
				t.Fatalf("expected negative min-tokens to be normalized to 0, got %d", r.MinTokens)
			}
		}
	}
	if !foundNegative {
		t.Fatal("expected negative-* rule to be kept after normalization")
	}
	for _, r := range cfg.Routing.TokenThresholdRules {
		if r.ModelPattern == "invalid-range-*" {
			t.Fatal("expected invalid-range-* rule to be dropped")
		}
	}
}

func TestRoutingTokenThresholdRulesUpperOnlyParsing(t *testing.T) {
	yamlData := `
routing:
  token-threshold-rules:
  - model-pattern: "opus-*"
    max-tokens: 1500
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
	if rule.MinTokens != 0 {
		t.Fatalf("expected min-tokens 0, got %d", rule.MinTokens)
	}
	if rule.MaxTokens != 1500 {
		t.Fatalf("expected max-tokens 1500, got %d", rule.MaxTokens)
	}
}

func TestRoutingTokenThresholdRulesLowerOnlyParsing(t *testing.T) {
	yamlData := `
routing:
  token-threshold-rules:
  - model-pattern: "opus-*"
    min-tokens: 2001
    billing-class: per-request
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(cfg.Routing.TokenThresholdRules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(cfg.Routing.TokenThresholdRules))
	}
	rule := cfg.Routing.TokenThresholdRules[0]
	if rule.MinTokens != 2001 {
		t.Fatalf("expected min-tokens 2001, got %d", rule.MinTokens)
	}
	if rule.MaxTokens != 0 {
		t.Fatalf("expected max-tokens 0 (unspecified), got %d", rule.MaxTokens)
	}
}

func TestSanitizeTokenThresholdRulesDropsEmptyRule(t *testing.T) {
	cfg := &Config{
		Routing: RoutingConfig{
			TokenThresholdRules: []TokenThresholdRule{
				{ModelPattern: "upper-only", MaxTokens: 100, BillingClass: BillingClassMetered},
				{ModelPattern: "lower-only", MinTokens: 50, BillingClass: BillingClassPerRequest},
				{ModelPattern: "empty", BillingClass: BillingClassMetered},
				{ModelPattern: "bounded", MinTokens: 10, MaxTokens: 90, BillingClass: BillingClassMetered},
			},
		},
	}
	cfg.SanitizeTokenThresholdRules()
	if len(cfg.Routing.TokenThresholdRules) != 3 {
		t.Fatalf("expected 3 rules after dropping empty, got %d", len(cfg.Routing.TokenThresholdRules))
	}
	for _, r := range cfg.Routing.TokenThresholdRules {
		if r.ModelPattern == "empty" {
			t.Fatal("expected empty rule to be dropped")
		}
	}
}

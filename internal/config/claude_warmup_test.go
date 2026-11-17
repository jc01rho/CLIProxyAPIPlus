package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// claude-warmup is a fork-only root: v8 management writes must accept it and a
// v8 migration must keep it live instead of commenting it out as unknown.
func TestClaudeWarmupSurvivesV8Layout(t *testing.T) {
	doc := "config-version: 8\nserver:\n  port: 8317\nclaude-warmup:\n  enabled: true\n  schedule: \"0 8 * * *\"\n  max-tokens: 10\n"
	if err := ValidateV8Config([]byte(doc)); err != nil {
		t.Fatalf("ValidateV8Config rejected claude-warmup: %v", err)
	}
	if err := ValidateV8Config([]byte(doc + "  bogus: 1\n")); err == nil {
		t.Fatal("ValidateV8Config accepted an unknown claude-warmup field")
	}

	legacy := "port: 8317\nclaude-warmup:\n  enabled: true\n  jitter: 0s\n"
	out, _, err := NormalizeConfigLayout([]byte(legacy), true)
	if err != nil {
		t.Fatalf("NormalizeConfigLayout: %v", err)
	}
	if strings.Contains(string(out), "# claude-warmup") {
		t.Fatalf("migration commented out claude-warmup:\n%s", out)
	}
	var migrated struct {
		ClaudeWarmup ClaudeWarmupConfig `yaml:"claude-warmup"`
	}
	if errUnmarshal := yaml.Unmarshal(out, &migrated); errUnmarshal != nil {
		t.Fatalf("unmarshal migrated: %v", errUnmarshal)
	}
	if !migrated.ClaudeWarmup.Enabled || migrated.ClaudeWarmup.Jitter != "0s" {
		t.Fatalf("migrated claude-warmup = %+v", migrated.ClaudeWarmup)
	}

	var root yaml.Node
	if errUnmarshal := yaml.Unmarshal([]byte("claude-warmup:\n  enabled: true\n"), &root); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if IsV8ConfigLayout(root.Content[0]) {
		t.Fatal("claude-warmup alone must not mark a document as v8")
	}
}

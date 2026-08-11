package config

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClineFreeModelsOnlyConfigSerialization(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("cline-free-models-only: true\n"), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if !cfg.ClineFreeModelsOnly {
		t.Fatal("ClineFreeModelsOnly = false, want true")
	}

	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(payload), `"cline-free-models-only":true`) {
		t.Fatalf("management JSON does not include cline-free-models-only: %s", payload)
	}
}

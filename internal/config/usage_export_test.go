package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageExportDefaultsAreSafeAndConfigRelative(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("usage-statistics-enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.UsageExport.Enabled || cfg.UsageExport.Mode != UsageExportModeDisabled {
		t.Fatalf("unsafe default usage-export state: %#v", cfg.UsageExport)
	}
	wantPath := filepath.Join(filepath.Dir(configPath), "keeper-outbox.db")
	if cfg.UsageExport.Outbox.Path != wantPath {
		t.Fatalf("outbox path = %q, want %q", cfg.UsageExport.Outbox.Path, wantPath)
	}
	if cfg.UsageExport.Outbox.MaxBytes != 1<<30 || cfg.UsageExport.Delivery.MaxBatchEvents != 500 || cfg.UsageExport.Metadata.IntervalMs != 300000 {
		t.Fatalf("unexpected defaults: %#v", cfg.UsageExport)
	}
	if cfg.UsageExport.Privacy.IncludeClientIP || cfg.UsageExport.Privacy.IncludeForwardedFor || cfg.UsageExport.Privacy.IncludeUserAgent {
		t.Fatalf("privacy defaults must exclude client metadata: %#v", cfg.UsageExport.Privacy)
	}
}

func TestUsageExportLoadValidation(t *testing.T) {
	valid := `usage-statistics-enabled: true
usage-export:
  enabled: true
  mode: push
  keeper:
    url: https://keeper.example.com/base
    token-env: CPA_KEEPER_TOKEN
    ca-file: null
    client-cert-file: null
    client-key-file: null
  outbox:
    path: OUTBOX
    max-bytes: 16777216
  delivery:
    max-batch-events: 100
    max-batch-bytes: 65536
    flush-interval-ms: 100
    request-timeout-ms: 1000
    initial-backoff-ms: 100
    max-backoff-ms: 100
  metadata:
    enabled: true
    interval-ms: 60000
    categories: [auth_files, api_keys, provider_identities]
  privacy:
    include-client-ip: false
    include-forwarded-for: false
    include-user-agent: false
`
	tests := []struct {
		name string
		edit func(string, string) string
	}{
		{name: "unsupported URL scheme", edit: func(s, outbox string) string {
			return strings.Replace(strings.Replace(s, "OUTBOX", outbox, 1), "https://keeper.example.com/base", "ftp://keeper.example.com", 1)
		}},
		{name: "unsafe env name", edit: func(s, outbox string) string {
			return strings.Replace(strings.Replace(s, "OUTBOX", outbox, 1), "CPA_KEEPER_TOKEN", "keeper-token", 1)
		}},
		{name: "missing usage statistics", edit: func(s, outbox string) string {
			return strings.Replace(strings.Replace(s, "OUTBOX", outbox, 1), "usage-statistics-enabled: true", "usage-statistics-enabled: false", 1)
		}},
		{name: "invalid delivery range", edit: func(s, outbox string) string {
			return strings.Replace(strings.Replace(s, "OUTBOX", outbox, 1), "max-batch-events: 100", "max-batch-events: 0", 1)
		}},
		{name: "relative outbox", edit: func(s, _ string) string { return strings.Replace(s, "OUTBOX", "relative.db", 1) }},
		{name: "outbox parent is file", edit: func(s, outbox string) string { return strings.Replace(s, "OUTBOX", outbox, 1) }},
		{name: "tls skip verify is unknown", edit: func(s, outbox string) string {
			s = strings.Replace(s, "OUTBOX", outbox, 1)
			return strings.Replace(s, "    client-key-file: null", "    client-key-file: null\n    tls-skip-verify: true", 1)
		}},
	}

	t.Run("HTTP URL", func(t *testing.T) {
		dir := t.TempDir()
		content := strings.Replace(strings.Replace(valid, "OUTBOX", filepath.Join(dir, "outbox.db"), 1), "https://keeper.example.com/base", "http://192.0.2.10:8080/keeper", 1)
		configPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(configPath); err != nil {
			t.Fatalf("LoadConfig() HTTP keeper URL error = %v", err)
		}
	})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outbox := filepath.Join(dir, "outbox.db")
			if tc.name == "outbox parent is file" {
				parentFile := filepath.Join(dir, "not-a-directory")
				if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				outbox = filepath.Join(parentFile, "outbox.db")
			}
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tc.edit(valid, outbox)), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(configPath); err == nil {
				t.Fatal("LoadConfig() error = nil, want usage-export validation error")
			}
		})
	}
}

func TestUsageExportAcceptsDirectKeeperToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`usage-statistics-enabled: true
usage-export:
  enabled: true
  mode: push
  keeper:
    url: http://192.168.0.50:8080
    token: direct-ingest-token
  outbox:
    path: /tmp/keeper-outbox.db
    max-bytes: 16777216
  delivery:
    max-batch-events: 100
    max-batch-bytes: 65536
    flush-interval-ms: 100
    request-timeout-ms: 1000
    initial-backoff-ms: 100
    max-backoff-ms: 100
  metadata:
    enabled: true
    interval-ms: 60000
    categories: [auth_files, api_keys, provider_identities]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.UsageExport.Keeper.UsageExportToken(); got != "direct-ingest-token" {
		t.Fatalf("direct keeper token = %q", got)
	}
	cfg.UsageExport.Keeper.TokenEnv = "CPA_KEEPER_INGEST_TOKEN"
	if err := cfg.ValidateUsageExport(configPath); err == nil {
		t.Fatal("expected direct token and token-env to be mutually exclusive")
	}
}

func TestUsageExportSaveDoesNotMaterializeDisabledDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("# retained\nusage-statistics-enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "usage-export:") {
		t.Fatalf("disabled usage-export defaults were materialized:\n%s", got)
	}
}

func TestUsageExportChangedDefaultBasenamePathSerializesRelativeToActualConfigDirectory(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("# retained\nusage-statistics-enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	changedPath := filepath.Join(configDir, "different", "keeper-outbox.db")
	cfg.UsageExport.Outbox.Path = changedPath
	if err = SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"usage-export:", "outbox:", "path: " + changedPath} {
		if !strings.Contains(string(got), fragment) {
			t.Fatalf("changed default-basename path %q was pruned:\n%s", fragment, got)
		}
	}
	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.UsageExport.Outbox.Path != changedPath {
		t.Fatalf("reloaded outbox path = %q, want %q", reloaded.UsageExport.Outbox.Path, changedPath)
	}
}

func TestUsageExportDisabledConfiguredValuesPersistAndUnavailableParentIsAllowed(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	unavailable := filepath.Join(t.TempDir(), "missing-mount", "configured.db")
	original := `usage-statistics-enabled: false
usage-export:
  enabled: false
  mode: disabled
  keeper:
    url: https://keeper.example.com/base
    token-env: CPA_KEEPER_TOKEN
    ca-file: /etc/ssl/custom.pem
    client-cert-file: null
    client-key-file: null
  outbox:
    path: ` + unavailable + `
    max-bytes: 16777216
  delivery:
    max-batch-events: 17
    max-batch-bytes: 65536
    flush-interval-ms: 321
    request-timeout-ms: 4321
    initial-backoff-ms: 222
    max-backoff-ms: 333
  metadata:
    enabled: false
    interval-ms: 60000
    categories: []
  privacy:
    include-client-ip: true
    include-forwarded-for: false
    include-user-agent: true
`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("disabled unavailable outbox must load: %v", err)
	}
	if err = SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"usage-export:", "url: https://keeper.example.com/base", "path: " + unavailable, "max-batch-events: 17", "include-client-ip: true", "include-user-agent: true"} {
		if !strings.Contains(string(got), fragment) {
			t.Fatalf("configured disabled value %q was pruned:\n%s", fragment, got)
		}
	}
}

func TestUsageExportRejectsWhitespaceAndTLSBypassVariantsButAllowsUnrelatedExtensions(t *testing.T) {
	base := func(keeperExtra string) string {
		return `usage-statistics-enabled: true
usage-export:
  enabled: true
  mode: push
  keeper:
    url: https://keeper.example.com
    token-env: CPA_KEEPER_TOKEN
    ca-file: null
    client-cert-file: null
    client-key-file: null
` + keeperExtra + `
  outbox:
    path: /tmp/keeper-outbox.db
    max-bytes: 16777216
  delivery:
    max-batch-events: 1
    max-batch-bytes: 65536
    flush-interval-ms: 100
    request-timeout-ms: 1000
    initial-backoff-ms: 100
    max-backoff-ms: 100
  metadata:
    enabled: true
    interval-ms: 60000
    categories: [auth_files]
  privacy:
    include-client-ip: false
    include-forwarded-for: false
    include-user-agent: false
`
	}
	for _, key := range []string{"tls-skip-verify", "skip-tls-verify", "tls_skip_verify", "TLS_Skip_Verify", "InsecureSkipVerify", "insecure-skip-verify"} {
		t.Run("bypass "+key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(base("    "+key+": true")), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatalf("LoadConfig() accepted TLS bypass %q", key)
			}
		})
	}
	for name, mutate := range map[string]func(string) string{
		"url":  func(s string) string { return strings.Replace(s, "url: https://", "url: ' https://", 1) + "'" },
		"env":  func(s string) string { return strings.Replace(s, "CPA_KEEPER_TOKEN", "'CPA_KEEPER_TOKEN '", 1) },
		"mode": func(s string) string { return strings.Replace(s, "mode: push", "mode: 'push '", 1) },
		"path": func(s string) string {
			return strings.Replace(s, "/tmp/keeper-outbox.db", "' /tmp/keeper-outbox.db'", 1)
		},
		"category": func(s string) string { return strings.Replace(s, "[auth_files]", "['auth_files ']", 1) },
	} {
		t.Run("whitespace "+name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(mutate(base(""))), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatalf("LoadConfig() accepted security-sensitive whitespace in %s", name)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(base("    future-keeper-option: retained")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("unrelated extension key rejected: %v", err)
	}
}

func TestUsageExportSavePreservesCommentsAndUnknownKeys(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	original := `# root comment
usage-statistics-enabled: true
usage-export:
  # keeper comment
  enabled: true
  mode: push
  keeper:
    url: https://keeper.example.com
    token-env: CPA_KEEPER_TOKEN
    ca-file: null
    client-cert-file: null
    client-key-file: null
    future-keeper-option: retained
  outbox:
    path: /tmp/keeper-outbox.db
    max-bytes: 16777216
  delivery:
    max-batch-events: 100
    max-batch-bytes: 65536
    flush-interval-ms: 100
    request-timeout-ms: 1000
    initial-backoff-ms: 100
    max-backoff-ms: 100
  metadata:
    enabled: true
    interval-ms: 60000
    categories: [auth_files, api_keys, provider_identities]
  privacy:
    include-client-ip: false
    include-forwarded-for: false
    include-user-agent: false
  future-section:
    value: retained
unknown-root:
  retained: true
`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.UsageExport.Delivery.FlushIntervalMs = 250
	if err := SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"# root comment", "# keeper comment", "future-keeper-option: retained", "future-section:", "unknown-root:", "flush-interval-ms: 250"} {
		if !strings.Contains(string(got), fragment) {
			t.Fatalf("saved YAML lost %q:\n%s", fragment, got)
		}
	}
}

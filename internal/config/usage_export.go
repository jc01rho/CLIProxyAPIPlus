package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	UsageExportModeDisabled = "disabled"
	UsageExportModePush     = "push"

	defaultUsageExportMaxBytes = int64(1 << 30)
)

var usageExportEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

// UsageExportConfig configures durable, outbound Keeper usage delivery.
type UsageExportConfig struct {
	Enabled  bool                      `yaml:"enabled" json:"enabled"`
	Mode     string                    `yaml:"mode" json:"mode"`
	Keeper   UsageExportKeeperConfig   `yaml:"keeper" json:"keeper"`
	Outbox   UsageExportOutboxConfig   `yaml:"outbox" json:"outbox"`
	Delivery UsageExportDeliveryConfig `yaml:"delivery" json:"delivery"`
	Metadata UsageExportMetadataConfig `yaml:"metadata" json:"metadata"`
	Privacy  UsageExportPrivacyConfig  `yaml:"privacy" json:"privacy"`
}

type UsageExportKeeperConfig struct {
	URL            string  `yaml:"url" json:"url"`
	Token          string  `yaml:"token" json:"-"`
	TokenEnv       string  `yaml:"token-env" json:"token-env"`
	CAFile         *string `yaml:"ca-file" json:"ca-file"`
	ClientCertFile *string `yaml:"client-cert-file" json:"client-cert-file"`
	ClientKeyFile  *string `yaml:"client-key-file" json:"client-key-file"`
}

type UsageExportOutboxConfig struct {
	Path     string `yaml:"path" json:"path"`
	MaxBytes int64  `yaml:"max-bytes" json:"max-bytes"`
}

type UsageExportDeliveryConfig struct {
	MaxBatchEvents   int64 `yaml:"max-batch-events" json:"max-batch-events"`
	MaxBatchBytes    int64 `yaml:"max-batch-bytes" json:"max-batch-bytes"`
	FlushIntervalMs  int64 `yaml:"flush-interval-ms" json:"flush-interval-ms"`
	RequestTimeoutMs int64 `yaml:"request-timeout-ms" json:"request-timeout-ms"`
	InitialBackoffMs int64 `yaml:"initial-backoff-ms" json:"initial-backoff-ms"`
	MaxBackoffMs     int64 `yaml:"max-backoff-ms" json:"max-backoff-ms"`
}

type UsageExportMetadataConfig struct {
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	IntervalMs int64    `yaml:"interval-ms" json:"interval-ms"`
	Categories []string `yaml:"categories" json:"categories"`
}

type UsageExportPrivacyConfig struct {
	IncludeClientIP     bool `yaml:"include-client-ip" json:"include-client-ip"`
	IncludeForwardedFor bool `yaml:"include-forwarded-for" json:"include-forwarded-for"`
	IncludeUserAgent    bool `yaml:"include-user-agent" json:"include-user-agent"`
}

func DefaultUsageExportConfig(configDir string) UsageExportConfig {
	if strings.TrimSpace(configDir) == "" {
		configDir = "."
	}
	absoluteDir, err := filepath.Abs(configDir)
	if err == nil {
		configDir = absoluteDir
	}
	return UsageExportConfig{
		Mode: UsageExportModeDisabled,
		Outbox: UsageExportOutboxConfig{
			Path:     filepath.Join(configDir, "keeper-outbox.db"),
			MaxBytes: defaultUsageExportMaxBytes,
		},
		Delivery: UsageExportDeliveryConfig{
			MaxBatchEvents:   500,
			MaxBatchBytes:    1 << 20,
			FlushIntervalMs:  1000,
			RequestTimeoutMs: 15000,
			InitialBackoffMs: 1000,
			MaxBackoffMs:     60000,
		},
		Metadata: UsageExportMetadataConfig{
			Enabled:    true,
			IntervalMs: 300000,
			Categories: []string{"auth_files", "api_keys", "provider_identities"},
		},
	}
}

func (cfg *Config) normalizeUsageExport(configFile string) error {
	if cfg == nil {
		return nil
	}
	if usageExportIsZero(cfg.UsageExport) {
		cfg.UsageExport = DefaultUsageExportConfig(filepath.Dir(configFile))
	}
	u := &cfg.UsageExport
	categories := append([]string(nil), u.Metadata.Categories...)
	sort.SliceStable(categories, func(i, j int) bool {
		return usageExportCategoryOrder(categories[i]) < usageExportCategoryOrder(categories[j])
	})
	u.Metadata.Categories = categories
	return cfg.ValidateUsageExport(configFile)
}

func (cfg *Config) ValidateUsageExport(configFile string) error {
	if cfg == nil {
		return nil
	}
	u := cfg.UsageExport
	for name, value := range map[string]string{
		"mode": u.Mode, "keeper.url": u.Keeper.URL, "keeper.token": u.Keeper.Token, "keeper.token-env": u.Keeper.TokenEnv, "outbox.path": u.Outbox.Path,
	} {
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("usage-export: %s must not contain leading or trailing whitespace", name)
		}
	}
	for name, value := range map[string]*string{"keeper.ca-file": u.Keeper.CAFile, "keeper.client-cert-file": u.Keeper.ClientCertFile, "keeper.client-key-file": u.Keeper.ClientKeyFile} {
		if value != nil && *value != strings.TrimSpace(*value) {
			return fmt.Errorf("usage-export: %s must not contain leading or trailing whitespace", name)
		}
	}
	for _, category := range u.Metadata.Categories {
		if category != strings.TrimSpace(category) {
			return fmt.Errorf("usage-export: metadata category must not contain leading or trailing whitespace")
		}
	}
	push := u.Enabled && u.Mode == UsageExportModePush
	disabled := !u.Enabled && u.Mode == UsageExportModeDisabled
	if !push && !disabled {
		return fmt.Errorf("usage-export: enabled and mode must be either false/disabled or true/push")
	}
	if push && !cfg.UsageStatisticsEnabled {
		return fmt.Errorf("usage-export: push requires usage-statistics-enabled: true")
	}
	if u.Keeper.URL != "" {
		parsed, err := url.Parse(u.Keeper.URL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(u.Keeper.URL) > 2048 {
			return fmt.Errorf("usage-export: keeper.url must be an absolute HTTP or HTTPS URL without userinfo, query, or fragment")
		}
	} else if push {
		return fmt.Errorf("usage-export: keeper.url is required in push mode")
	}
	if u.Keeper.Token != "" && u.Keeper.TokenEnv != "" {
		return fmt.Errorf("usage-export: exactly one of keeper.token or keeper.token-env may be set")
	}
	if u.Keeper.Token != "" {
		if len(u.Keeper.Token) > 4096 || strings.ContainsAny(u.Keeper.Token, "\r\n") {
			return fmt.Errorf("usage-export: keeper.token must be a single-line token no longer than 4096 bytes")
		}
	} else if u.Keeper.TokenEnv != "" {
		if !usageExportEnvName.MatchString(u.Keeper.TokenEnv) {
			return fmt.Errorf("usage-export: keeper.token-env must be a safe POSIX environment name")
		}
	} else if push {
		return fmt.Errorf("usage-export: one of keeper.token or keeper.token-env is required in push mode")
	}
	for name, value := range map[string]*string{"ca-file": u.Keeper.CAFile, "client-cert-file": u.Keeper.ClientCertFile, "client-key-file": u.Keeper.ClientKeyFile} {
		if value != nil && (!filepath.IsAbs(*value) || *value == "" || len(*value) > 4096) {
			return fmt.Errorf("usage-export: keeper.%s must be an absolute path or null", name)
		}
	}
	if (u.Keeper.ClientCertFile == nil) != (u.Keeper.ClientKeyFile == nil) {
		return fmt.Errorf("usage-export: client certificate and key paths must be configured together")
	}
	if !filepath.IsAbs(u.Outbox.Path) || len(u.Outbox.Path) > 4096 {
		return fmt.Errorf("usage-export: outbox.path must be absolute")
	}
	if u.Outbox.MaxBytes < 16<<20 || u.Outbox.MaxBytes > 1<<40 {
		return fmt.Errorf("usage-export: outbox.max-bytes must be between 16 MiB and 1 TiB")
	}
	if push {
		if err := validateOutboxPath(u.Outbox.Path); err != nil {
			return fmt.Errorf("usage-export: outbox.path: %w", err)
		}
	}
	if u.Delivery.MaxBatchEvents < 1 || u.Delivery.MaxBatchEvents > 500 || u.Delivery.MaxBatchBytes < 65536 || u.Delivery.MaxBatchBytes > 1<<20 || u.Delivery.FlushIntervalMs < 100 || u.Delivery.FlushIntervalMs > 60000 || u.Delivery.RequestTimeoutMs < 1000 || u.Delivery.RequestTimeoutMs > 120000 || u.Delivery.InitialBackoffMs < 100 || u.Delivery.InitialBackoffMs > 60000 || u.Delivery.MaxBackoffMs < u.Delivery.InitialBackoffMs || u.Delivery.MaxBackoffMs > 900000 {
		return fmt.Errorf("usage-export: delivery settings are outside supported ranges")
	}
	if u.Metadata.IntervalMs < 60000 || u.Metadata.IntervalMs > 86400000 {
		return fmt.Errorf("usage-export: metadata.interval-ms is outside supported range")
	}
	seen := map[string]bool{}
	for _, category := range u.Metadata.Categories {
		if usageExportCategoryOrder(category) > 2 || seen[category] {
			return fmt.Errorf("usage-export: metadata.categories contains an invalid or duplicate category")
		}
		seen[category] = true
	}
	if u.Metadata.Enabled && len(u.Metadata.Categories) == 0 {
		return fmt.Errorf("usage-export: metadata.categories must not be empty when metadata is enabled")
	}
	_ = configFile
	return nil
}

// UsageExportToken returns the configured direct token, or falls back to the
// configured environment variable. Callers must never log the returned value.
func (cfg UsageExportKeeperConfig) UsageExportToken() string {
	if cfg.Token != "" {
		return cfg.Token
	}
	return strings.TrimSpace(os.Getenv(cfg.TokenEnv))
}

func (cfg UsageExportKeeperConfig) UsageExportTokenConfigured() bool {
	return cfg.UsageExportToken() != ""
}

func usageExportMatchesSafeDefaults(cfg UsageExportConfig, configDir string) bool {
	if strings.TrimSpace(configDir) == "" {
		return false
	}
	return reflect.DeepEqual(cfg, DefaultUsageExportConfig(configDir))
}

func usageExportIsZero(cfg UsageExportConfig) bool {
	return !cfg.Enabled && cfg.Mode == "" && cfg.Keeper == (UsageExportKeeperConfig{}) && cfg.Outbox == (UsageExportOutboxConfig{}) && cfg.Delivery == (UsageExportDeliveryConfig{}) && !cfg.Metadata.Enabled && cfg.Metadata.IntervalMs == 0 && len(cfg.Metadata.Categories) == 0 && cfg.Privacy == (UsageExportPrivacyConfig{})
}

func usageExportCategoryOrder(category string) int {
	switch category {
	case "auth_files":
		return 0
	case "api_keys":
		return 1
	case "provider_identities":
		return 2
	default:
		return 99
	}
}

func rejectUsageExportTLSBypassYAML(data []byte) error {
	var root struct {
		UsageExport map[string]any `yaml:"usage-export"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil
	}
	keeper, _ := root.UsageExport["keeper"].(map[string]any)
	for key := range keeper {
		normalized := normalizeSecurityKey(key)
		if normalized == "tlsskipverify" || normalized == "skiptlsverify" || normalized == "insecureskipverify" || normalized == "insecuretlsskipverify" {
			return fmt.Errorf("usage-export: %s is forbidden", key)
		}
	}
	return nil
}

func normalizeSecurityKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validateOutboxPath(path string) error {
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("points to a directory")
		}
		if info.Mode().Perm()&0o222 == 0 {
			return fmt.Errorf("existing file is not writable")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("parent directory is unavailable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("parent is not a directory")
	}
	if info.Mode().Perm()&0o222 == 0 {
		return fmt.Errorf("parent directory is not writable")
	}
	return nil
}

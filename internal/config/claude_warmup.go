package config

import (
	"strings"
	"time"
)

// Claude warmup defaults.
const (
	DefaultClaudeWarmupSchedule  = "50 7 * * 1-5"
	DefaultClaudeWarmupTimezone  = "Asia/Seoul"
	DefaultClaudeWarmupModel     = "claude-haiku-4-5-20251001"
	DefaultClaudeWarmupMaxTokens = 25
	DefaultClaudeWarmupJitter    = 60 * time.Second
)

// ClaudeWarmupConfig schedules one minimal request per Claude OAuth credential so
// each account's 5-hour usage window starts at a predictable time.
type ClaudeWarmupConfig struct {
	// Enabled turns the scheduled warmup on. Default false.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Schedule is a five-field cron expression (minute hour day-of-month month day-of-week).
	Schedule string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	// Timezone is the IANA zone the schedule is evaluated in.
	Timezone string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	// Model is the Claude model used for the warmup request.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
	// MaxTokens caps the warmup completion length.
	MaxTokens int `yaml:"max-tokens,omitempty" json:"max-tokens,omitempty"`
	// Jitter is the maximum random delay between two accounts (Go duration, e.g. "60s").
	// "0s" disables the delay.
	Jitter string `yaml:"jitter,omitempty" json:"jitter,omitempty"`
}

// WithDefaults returns a copy with empty fields replaced by their defaults.
func (c ClaudeWarmupConfig) WithDefaults() ClaudeWarmupConfig {
	if strings.TrimSpace(c.Schedule) == "" {
		c.Schedule = DefaultClaudeWarmupSchedule
	}
	if strings.TrimSpace(c.Timezone) == "" {
		c.Timezone = DefaultClaudeWarmupTimezone
	}
	if strings.TrimSpace(c.Model) == "" {
		c.Model = DefaultClaudeWarmupModel
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = DefaultClaudeWarmupMaxTokens
	}
	if strings.TrimSpace(c.Jitter) == "" {
		c.Jitter = DefaultClaudeWarmupJitter.String()
	}
	return c
}

// JitterDuration parses Jitter, falling back to the default for invalid or negative values.
func (c ClaudeWarmupConfig) JitterDuration() time.Duration {
	raw := strings.TrimSpace(c.Jitter)
	if raw == "" {
		return DefaultClaudeWarmupJitter
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return DefaultClaudeWarmupJitter
	}
	return d
}

// Location loads Timezone, falling back to the default zone and then UTC.
func (c ClaudeWarmupConfig) Location() *time.Location {
	for _, name := range []string{strings.TrimSpace(c.Timezone), DefaultClaudeWarmupTimezone} {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.UTC
}

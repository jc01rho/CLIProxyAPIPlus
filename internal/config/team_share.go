package config

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"gopkg.in/yaml.v3"
)

// Team seat tiers understood by the team-share pool.
const (
	TeamShareTierStandard = "standard"
	TeamShareTierPremium  = "premium"
)

// TeamShareConfig groups several Claude Team seat OAuth credentials into pools
// and allots each pool's usage to client API keys by percentage.
type TeamShareConfig struct {
	// Enabled turns on usage accounting, per-member enforcement and pool-aware routing.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// StateFile persists window counters and calibration across restarts.
	// Empty keeps the state next to the config file (team-share-state.json).
	StateFile string `yaml:"state-file,omitempty" json:"state-file,omitempty"`
	// AuditLog is an append-only JSONL file for join/leave and re-auth events.
	AuditLog string `yaml:"audit-log,omitempty" json:"audit-log,omitempty"`
	// Tiers overrides the default per-seat limit estimates (API-equivalent USD per window).
	Tiers map[string]TeamShareTierLimit `yaml:"tiers,omitempty" json:"tiers,omitempty"`
	// Pricing overrides the public API price table. Keys are lower-case model
	// substrings; the longest matching key wins and beats the built-in table.
	Pricing map[string]TeamSharePrice `yaml:"pricing,omitempty" json:"pricing,omitempty"`
	// Pools lists the credential pools.
	Pools []TeamSharePool `yaml:"pools,omitempty" json:"pools,omitempty"`
}

// TeamShareTierLimit is the default limit estimate for one seat tier. It is
// replaced by the measured calibration once enough windows have been observed.
type TeamShareTierLimit struct {
	FiveHourUSD float64 `yaml:"five-hour-usd" json:"five-hour-usd"`
	WeeklyUSD   float64 `yaml:"weekly-usd" json:"weekly-usd"`
}

// TeamSharePrice is a per-million-token USD price. Zero cache prices derive
// from Input (read 0.1x, 5-minute write 1.25x).
type TeamSharePrice struct {
	Input      float64 `yaml:"input" json:"input"`
	Output     float64 `yaml:"output" json:"output"`
	CacheRead  float64 `yaml:"cache-read,omitempty" json:"cache-read,omitempty"`
	CacheWrite float64 `yaml:"cache-write,omitempty" json:"cache-write,omitempty"`
}

// TeamSharePool is one set of seat credentials and the keys sharing it.
type TeamSharePool struct {
	Name     string             `yaml:"name" json:"name"`
	Accounts []TeamShareAccount `yaml:"accounts" json:"accounts"`
	Members  []TeamShareMember  `yaml:"members" json:"members"`
}

// TeamShareAccount references one OAuth credential. In YAML it may be a bare
// string (the credential identifier) or a mapping with an explicit tier.
type TeamShareAccount struct {
	// Auth matches a credential by id, auth index, file name or email.
	Auth string `yaml:"auth" json:"auth"`
	// Tier is "standard" (default) or "premium".
	Tier string `yaml:"tier,omitempty" json:"tier,omitempty"`
}

// UnmarshalYAML accepts the scalar shorthand.
func (a *TeamShareAccount) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		a.Auth = strings.TrimSpace(node.Value)
		return nil
	}
	type plain TeamShareAccount
	var tmp plain
	if err := node.Decode(&tmp); err != nil {
		return err
	}
	*a = TeamShareAccount(tmp)
	return nil
}

// TeamShareMember is one API key and its share of the pool.
type TeamShareMember struct {
	// Name is a display label.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// APIKey is the client API key; KeyID may be used instead so the secret
	// does not appear in the pool definition (see teamshare.KeyID).
	APIKey string `yaml:"api-key,omitempty" json:"api-key,omitempty"`
	KeyID  string `yaml:"key-id,omitempty" json:"key-id,omitempty"`
	// Percent is the member's share of the pool total, 0-100.
	Percent float64 `yaml:"percent" json:"percent"`
}

// NormalizeTeamShareTier maps free-form tier text to a known tier.
func NormalizeTeamShareTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "premium", "prem", "team-premium":
		return TeamShareTierPremium
	default:
		return TeamShareTierStandard
	}
}

// Validate checks pool structure. Members' percentages must sum to at most 100.
func (c TeamShareConfig) Validate() error {
	seen := map[string]struct{}{}
	for i, pool := range c.Pools {
		name := strings.TrimSpace(pool.Name)
		if name == "" {
			return fmt.Errorf("team-share.pools[%d]: name is required", i)
		}
		if _, dup := seen[strings.ToLower(name)]; dup {
			return fmt.Errorf("team-share.pools[%d]: duplicate pool name %q", i, name)
		}
		seen[strings.ToLower(name)] = struct{}{}
		for j, acc := range pool.Accounts {
			if strings.TrimSpace(acc.Auth) == "" {
				return fmt.Errorf("team-share pool %q accounts[%d]: auth is required", name, j)
			}
		}
		total := 0.0
		keys := map[string]struct{}{}
		for j, m := range pool.Members {
			if strings.TrimSpace(m.APIKey) == "" && strings.TrimSpace(m.KeyID) == "" {
				return fmt.Errorf("team-share pool %q members[%d]: api-key or key-id is required", name, j)
			}
			if math.IsNaN(m.Percent) || m.Percent < 0 || m.Percent > 100 {
				return fmt.Errorf("team-share pool %q members[%d]: percent must be within 0-100", name, j)
			}
			id := strings.TrimSpace(m.KeyID)
			if id == "" {
				id = "key:" + strings.TrimSpace(m.APIKey)
			}
			if _, dup := keys[id]; dup {
				return fmt.Errorf("team-share pool %q members[%d]: duplicate member key", name, j)
			}
			keys[id] = struct{}{}
			total += m.Percent
		}
		if total > 100+1e-9 {
			return fmt.Errorf("team-share pool %q: member percent sum %.2f exceeds 100", name, total)
		}
	}
	for name, p := range c.Pricing {
		if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0 {
			return errors.New("team-share.pricing[" + name + "]: prices must not be negative")
		}
	}
	return nil
}

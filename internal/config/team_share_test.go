package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadTeamShareYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(path)
}

func TestTeamShareYAMLAcceptsScalarAccountsAndKeyIDs(t *testing.T) {
	cfg, err := loadTeamShareYAML(t, `
team-share:
  enabled: true
  pools:
    - name: team
      accounts:
        - one@junghap.top
        - {auth: two@junghap.top, tier: premium}
      members:
        - {name: a, api-key: sk-a, percent: 60}
        - {name: b, key-id: abcdef123456, percent: 40}
`)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	pool := cfg.TeamShare.Pools[0]
	if len(pool.Accounts) != 2 || pool.Accounts[0].Auth != "one@junghap.top" || pool.Accounts[1].Tier != "premium" {
		t.Fatalf("accounts = %+v", pool.Accounts)
	}
	if pool.Members[1].KeyID != "abcdef123456" || pool.Members[1].Percent != 40 {
		t.Fatalf("members = %+v", pool.Members)
	}
}

func TestTeamShareRejectsPercentSumOverHundred(t *testing.T) {
	_, err := loadTeamShareYAML(t, `
team-share:
  enabled: true
  pools:
    - name: team
      accounts: [one@junghap.top]
      members:
        - {api-key: sk-a, percent: 70}
        - {api-key: sk-b, percent: 40}
`)
	if err == nil || !strings.Contains(err.Error(), "exceeds 100") {
		t.Fatalf("err = %v, want percent sum rejection", err)
	}
}

func TestTeamShareValidateRejectsMissingMemberKey(t *testing.T) {
	cfg := TeamShareConfig{Pools: []TeamSharePool{{
		Name:     "team",
		Accounts: []TeamShareAccount{{Auth: "x"}},
		Members:  []TeamShareMember{{Percent: 10}},
	}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a member without api-key or key-id must be rejected")
	}
}

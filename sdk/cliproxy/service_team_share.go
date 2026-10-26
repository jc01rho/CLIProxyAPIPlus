package cliproxy

import (
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

func (s *Service) teamShareAccounts() []teamshare.AccountInfo {
	if s == nil || s.coreManager == nil {
		return nil
	}
	auths := s.coreManager.List()
	out := make([]teamshare.AccountInfo, 0, len(auths))
	for _, a := range auths {
		if a == nil || !strings.EqualFold(strings.TrimSpace(a.Provider), "claude") {
			continue
		}
		email := ""
		if raw, ok := a.Metadata["email"].(string); ok {
			email = strings.TrimSpace(raw)
		}
		if email == "" && a.Attributes != nil {
			email = strings.TrimSpace(a.Attributes["email"])
		}
		out = append(out, teamshare.AccountInfo{
			ID:       a.ID,
			Index:    a.EnsureIndex(),
			Email:    email,
			FileName: filepath.Base(a.FileName),
			Provider: a.Provider,
			Disabled: a.Disabled || a.Status == coreauth.StatusDisabled,
		})
	}
	return out
}

func (s *Service) startTeamShare(cfg *config.Config) {
	engine := teamshare.Default()
	engine.SetAccountLister(s.teamShareAccounts)
	usage.RegisterNamedPlugin("team-share", engine)
	s.applyTeamShare(cfg)
	if err := engine.Load(); err != nil {
		log.WithError(err).Warn("team-share: saved state could not be restored")
	}
}

func (s *Service) applyTeamShare(cfg *config.Config) {
	if cfg == nil {
		return
	}
	engine := teamshare.Default()
	ts := cfg.TeamShare
	if ts.Enabled && strings.TrimSpace(s.configPath) != "" {
		dir := filepath.Dir(s.configPath)
		if strings.TrimSpace(ts.StateFile) == "" {
			ts.StateFile = filepath.Join(dir, "team-share-state.json")
		}
		if strings.TrimSpace(ts.AuditLog) == "" {
			ts.AuditLog = filepath.Join(dir, "team-share-audit.log")
		}
	}
	if !ts.Enabled {
		ts.StateFile = ""
	}
	engine.SetStatePath(ts.StateFile)
	if err := engine.Apply(ts); err != nil {
		log.WithError(err).Error("team-share: configuration rejected, keeping the previous one")
		return
	}
}

func (s *Service) stopTeamShare() {
	if err := teamshare.Default().Flush(); err != nil {
		log.WithError(err).Warn("team-share: final state flush failed")
	}
}

func teamShareRemaining(authID string) (float64, bool) {
	return teamshare.Default().RemainingFraction(authID)
}

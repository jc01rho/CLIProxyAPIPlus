package cliproxy

import "github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"

func (s *Service) keeperExportSnapshot() keeperexport.SnapshotInput {
	var snapshot keeperexport.SnapshotInput
	if s == nil {
		return snapshot
	}
	s.cfgMu.RLock()
	if s.cfg != nil {
		if cloned := s.cfg.CloneForRuntime(); cloned != nil {
			snapshot.Config = *cloned
		}
	}
	s.cfgMu.RUnlock()
	if s.coreManager != nil {
		snapshot.Auths = s.coreManager.List()
	}
	return snapshot
}

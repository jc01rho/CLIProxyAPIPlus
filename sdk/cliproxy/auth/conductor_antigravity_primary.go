package auth

import (
	"sort"
	"strings"
	"time"
)

// soleAntigravityPrimaryWinner returns the single antigravity credential that should
// serve traffic among the primary/standby-managed family, or nil when the invariant
// does not force one. Credentials without PrimaryInfo are legacy/fallback credentials
// outside the scheme and are ignored here — the invalid-grant fallback lane relies on
// them staying enabled. Winner resolution:
//   - an enabled primary claimant (IsPrimary, not disabled) wins, lowest Order then ID;
//   - otherwise a sole enabled credential with PrimaryInfo wins;
//   - with no enabled PrimaryInfo credential the invariant cannot force a winner.
func soleAntigravityPrimaryWinner(auths []*Auth) *Auth {
	antigravity := make([]*Auth, 0)
	for _, auth := range auths {
		if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") && auth.PrimaryInfo != nil {
			antigravity = append(antigravity, auth)
		}
	}
	if len(antigravity) == 0 {
		return nil
	}

	enabled := func(auth *Auth) bool {
		return !auth.Disabled && auth.Status != StatusDisabled
	}

	claimants := make([]*Auth, 0)
	for _, auth := range antigravity {
		if auth.PrimaryInfo != nil && auth.PrimaryInfo.IsPrimary && enabled(auth) {
			claimants = append(claimants, auth)
		}
	}
	if len(claimants) > 0 {
		sort.Slice(claimants, func(i, j int) bool {
			oi, oj := claimants[i].PrimaryInfo.Order, claimants[j].PrimaryInfo.Order
			if oi != oj {
				return oi < oj
			}
			return claimants[i].ID < claimants[j].ID
		})
		return claimants[0]
	}

	enabledAuths := make([]*Auth, 0)
	for _, auth := range antigravity {
		if enabled(auth) {
			enabledAuths = append(enabledAuths, auth)
		}
	}
	if len(enabledAuths) == 1 {
		return enabledAuths[0]
	}
	return nil
}

// soleAntigravityPrimaryExcluded reports whether the given candidate must be excluded
// from selection so exactly one antigravity credential serves traffic. The winner is
// computed from the candidate slice itself, so the rule holds in every selection lane
// (route-model, across-priorities, and weighted-robin) without re-entrant locking.
func soleAntigravityPrimaryExcluded(provider string, auths []*Auth, candidate *Auth) bool {
	if !strings.EqualFold(strings.TrimSpace(provider), "antigravity") {
		return false
	}
	if candidate == nil {
		return false
	}
	winner := soleAntigravityPrimaryWinner(auths)
	if winner == nil {
		return false
	}
	return candidate.ID != winner.ID
}

// reconcileSoleAntigravityPrimaryLocked enforces the sole-primary invariant across the
// primary/standby-managed antigravity family (credentials carrying PrimaryInfo):
// exactly one enabled credential carrying IsPrimary, all other managed credentials
// disabled standbys. preferredID, when non-empty, names a credential the caller
// explicitly promoted (e.g. a management enable) and it wins over the current primary.
// Legacy credentials without PrimaryInfo are outside the scheme (invalid-grant fallback
// relies on them) and are left untouched; the runtime selection guard still demotes a
// managed duplicate-claim loser per request. Caller must hold m.mu.
// Returns the auths whose role changed so the caller can persist them after releasing
// the lock.
func (m *Manager) reconcileSoleAntigravityPrimaryLocked(preferredID string) []*Auth {
	antigravity := make([]*Auth, 0)
	for _, auth := range m.auths {
		if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") && auth.PrimaryInfo != nil {
			antigravity = append(antigravity, auth)
		}
	}
	if len(antigravity) == 0 {
		return nil
	}

	winner := soleAntigravityPrimaryWinner(antigravity)
	for _, auth := range antigravity {
		if preferredID != "" && auth.ID == preferredID {
			winner = auth
			break
		}
	}
	if winner == nil {
		return nil
	}

	changed := make([]*Auth, 0)
	for _, auth := range antigravity {
		if auth.ID == winner.ID {
			if auth.Disabled || auth.Status != StatusActive ||
				auth.PrimaryInfo == nil || !auth.PrimaryInfo.IsPrimary {
				auth.Disabled = false
				auth.Status = StatusActive
				auth.StatusMessage = ""
				auth.PrimaryInfo = &PrimaryInfo{IsPrimary: true, Order: 1}
				SyncPrimaryInfoMetadata(auth)
				changed = append(changed, auth)
			}
			continue
		}
		claimsPrimary := auth.PrimaryInfo != nil && auth.PrimaryInfo.IsPrimary
		enabled := !auth.Disabled && auth.Status != StatusDisabled
		if !claimsPrimary && !enabled {
			continue
		}
		if auth.PrimaryInfo == nil {
			auth.PrimaryInfo = &PrimaryInfo{IsPrimary: false, Order: 0}
		}
		auth.PrimaryInfo.IsPrimary = false
		auth.Disabled = true
		auth.Status = StatusDisabled
		auth.StatusMessage = "standby: antigravity primary/standby handoff enforced"
		auth.Unavailable = false
		auth.NextRetryAfter = time.Time{}
		auth.Quota = QuotaState{}
		SyncPrimaryInfoMetadata(auth)
		changed = append(changed, auth)
	}
	return changed
}

// soleAntigravityPrimaryExcludedAmong is the map-backed form used by the selection
// candidate loops that scan the full manager state under m.mu.
func soleAntigravityPrimaryExcludedAmong(provider string, auths map[string]*Auth, candidate *Auth) bool {
	if candidate == nil {
		return false
	}
	peers := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		peers = append(peers, auth)
	}
	return soleAntigravityPrimaryExcluded(provider, peers, candidate)
}

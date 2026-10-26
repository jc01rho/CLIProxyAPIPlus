package teamshare

import (
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

const windowJitter = 2 * time.Minute

// Sample is one finished upstream request attributed to a credential and a client key.
type Sample struct {
	AuthID     string
	AuthIndex  string
	Email      string
	APIKey     string
	Model      string
	Usage      Usage
	Headers    http.Header
	Failed     bool
	StatusCode int
	At         time.Time
}

// Record folds one request into the account ledger, the calibration and the member counters.
func (e *Engine) Record(s Sample) {
	if e == nil || strings.TrimSpace(s.AuthID) == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled {
		return
	}
	now := s.At
	if now.IsZero() {
		now = e.now()
	}
	st := e.account(s.AuthID, true)
	if s.AuthIndex != "" {
		st.Index = s.AuthIndex
	}
	if s.Email != "" {
		st.Email = s.Email
	}
	st.LastSeen = now
	e.dirty = true

	if s.Failed && (s.StatusCode == http.StatusUnauthorized || s.StatusCode == http.StatusForbidden) {
		reason := ReasonUnauthorized
		if s.StatusCode == http.StatusForbidden {
			reason = ReasonForbidden
		}
		e.markReauthLocked(st, reason, now)
		return
	}
	if !s.Failed && st.Reauth && (st.ReauthReason == ReasonUnauthorized || st.ReauthReason == ReasonForbidden) {
		st.Reauth, st.ReauthReason = false, ""
	}

	obs := ParseObservation(s.Headers)
	e.syncWindow(st, WindowFiveHour, obs.FiveHour.Reset, now)
	e.syncWindow(st, WindowWeekly, obs.Weekly.Reset, now)

	cost := 0.0
	if !s.Failed {
		cost = e.prices.CostUSD(s.Model, s.Usage)
		e.addCostLocked(st, s, cost, now)
	}

	e.applyUtilization(st, WindowFiveHour, obs.FiveHour, now)
	e.applyUtilization(st, WindowWeekly, obs.Weekly, now)
	e.maybeFlushLocked(now)
}

func (e *Engine) addCostLocked(st *accountState, s Sample, cost float64, now time.Time) {
	model := strings.TrimSpace(s.Model)
	if model == "" {
		model = "unknown"
	}
	mt := st.Models[model]
	if mt == nil {
		mt = &ModelTotals{}
		st.Models[model] = mt
	}
	mt.Requests++
	mt.Input += s.Usage.Input
	mt.Output += s.Usage.Output
	mt.CacheRead += s.Usage.CacheRead
	mt.CacheWrite += s.Usage.CacheWrite
	mt.CostUSD += cost
	st.Requests++
	st.TotalCostUSD += cost
	st.FiveHour.CostUSD += cost
	st.Weekly.CostUSD += cost

	keyID := ""
	if strings.TrimSpace(s.APIKey) != "" {
		keyID = KeyID(s.APIKey)
	}
	infos := e.accountList(now)
	for _, pool := range e.cfg.Pools {
		if !poolHasAuth(pool, s.AuthID, s.AuthIndex, infos) {
			continue
		}
		ps := e.pools[pool.Name]
		if ps == nil {
			continue
		}
		e.rollPoolLocked(pool, ps, now)
		var member *memberState
		for _, m := range pool.Members {
			if keyID != "" && memberID(m) == keyID {
				member = ps.Members[memberID(m)]
				break
			}
		}
		if member == nil {
			ps.FiveHour.SharedUSD += cost
			ps.Weekly.SharedUSD += cost
			ps.FiveHour.SharedReqs++
			continue
		}
		member.FiveHour.UsedUSD += cost
		member.Weekly.UsedUSD += cost
		member.Requests++
		member.Total += cost
	}
}

func poolHasAuth(pool config.TeamSharePool, id, index string, infos []AccountInfo) bool {
	for _, acc := range pool.Accounts {
		ref := strings.TrimSpace(acc.Auth)
		if strings.EqualFold(ref, id) || (index != "" && strings.EqualFold(ref, index)) {
			return true
		}
		for _, info := range infos {
			if info.ID == id && info.matches(ref) {
				return true
			}
		}
	}
	return false
}

// syncWindow rolls an account window when the observed reset moved or the stored one passed.
func (e *Engine) syncWindow(st *accountState, name string, reset time.Time, now time.Time) {
	w := st.window(name)
	switch {
	case !w.ResetAt.IsZero() && !now.Before(w.ResetAt) && (reset.IsZero() || !reset.After(w.ResetAt)):
		e.rollAccountWindow(st, name, time.Time{}, now)
	case !reset.IsZero() && !w.ResetAt.IsZero() && reset.Sub(w.ResetAt) > windowJitter:
		e.rollAccountWindow(st, name, reset, now)
	}
	if !reset.IsZero() && w.ResetAt.IsZero() {
		w.ResetAt = reset
	}
}

// rollAccountWindow closes the current window, folds its calibration and starts a fresh,
// fully tracked one (anchored at zero cost and zero utilization).
func (e *Engine) rollAccountWindow(st *accountState, name string, nextReset time.Time, now time.Time) {
	w := st.window(name)
	if w.CurrentEst > 0 && !e.planChangedLocked(st, name, w.CurrentEst, now) {
		if w.CalibN == 0 {
			w.Calib = w.CurrentEst
		} else {
			w.Calib = (1-ewmaAlpha)*w.Calib + ewmaAlpha*w.CurrentEst
		}
		w.CalibN++
	}
	w.ResetAt = nextReset
	w.CostUSD = 0
	w.Utilization = 0
	w.UtilSeen = false
	w.CurrentEst = 0
	w.Anchored = true
	w.AnchorUtil = 0
	w.AnchorCost = 0
}

func (e *Engine) applyUtilization(st *accountState, name string, obs WindowObservation, now time.Time) {
	if !obs.HasUtil {
		return
	}
	w := st.window(name)
	w.Utilization = obs.Utilization
	w.UtilSeen = true
	w.ObservedAt = now
	if !w.Anchored {
		w.Anchored = true
		w.AnchorUtil = obs.Utilization
		w.AnchorCost = w.CostUSD
		return
	}
	du := obs.Utilization - w.AnchorUtil
	dc := w.CostUSD - w.AnchorCost
	if du < provisionalMinUtil || dc <= 0 {
		return
	}
	est := dc / du
	if e.planChangedLocked(st, name, est, now) {
		return
	}
	w.CurrentEst = est
}

// planChangedLocked flags the account when a new estimate departs sharply from the calibrated one.
func (e *Engine) planChangedLocked(st *accountState, name string, est float64, now time.Time) bool {
	w := st.window(name)
	if w.CalibN < 2 || w.Calib <= 0 || est <= 0 {
		return false
	}
	if est <= w.Calib*planChangeFactor && est >= w.Calib/planChangeFactor {
		return false
	}
	e.markReauthLocked(st, ReasonPlanChanged, now)
	return true
}

func (e *Engine) markReauthLocked(st *accountState, reason string, now time.Time) {
	if st.Reauth && st.ReauthReason == reason {
		return
	}
	st.Reauth = true
	st.ReauthReason = reason
	st.ReauthAt = now
	e.dirty = true
	if e.auditPath != "" {
		entry := AuditEntry{Time: now, Actor: "team-share", Action: "reauth-required", Name: st.Email, Detail: map[string]string{"account": st.ID, "reason": reason}}
		if err := AppendAudit(e.auditPath, entry); err != nil {
			log.WithError(err).Warn("team-share: audit append failed")
		}
	}
}

// AckReauth clears a re-authentication flag and restarts calibration, after the operator
// re-registered the credential or confirmed the new plan.
func (e *Engine) AckReauth(authID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.accounts[authID]
	if st == nil {
		return false
	}
	st.Reauth, st.ReauthReason = false, ""
	for _, name := range []string{WindowFiveHour, WindowWeekly} {
		w := st.window(name)
		w.Calib, w.CalibN, w.CurrentEst = 0, 0, 0
	}
	e.dirty = true
	return true
}

func (e *Engine) rollPoolLocked(pool config.TeamSharePool, ps *poolState, now time.Time) {
	for _, name := range []string{WindowFiveHour, WindowWeekly} {
		pw := ps.window(name)
		dur := windowDuration(name)
		if pw.ResetAt.IsZero() {
			pw.ResetAt = e.anchorResetLocked(pool, name, now)
			continue
		}
		if now.Before(pw.ResetAt) {
			continue
		}
		for _, m := range ps.Members {
			if name == WindowWeekly {
				m.Weekly.UsedUSD = 0
			} else {
				m.FiveHour.UsedUSD = 0
			}
		}
		pw.SharedUSD, pw.SharedReqs = 0, 0
		for !now.Before(pw.ResetAt) {
			pw.ResetAt = pw.ResetAt.Add(dur)
		}
	}
}

// anchorResetLocked picks the first reset of a pool window: the earliest future reset
// reported by an active account, else one full window from now.
func (e *Engine) anchorResetLocked(pool config.TeamSharePool, name string, now time.Time) time.Time {
	var best time.Time
	for _, pa := range e.resolvePool(pool, now) {
		if !pa.included || pa.state == nil {
			continue
		}
		reset := pa.state.window(name).ResetAt
		if reset.After(now) && (best.IsZero() || reset.Before(best)) {
			best = reset
		}
	}
	if best.IsZero() {
		return now.Add(windowDuration(name))
	}
	return best
}

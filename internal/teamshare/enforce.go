package teamshare

import (
	"math"
	"strings"
	"time"
)

// Decision is the outcome of checking one client key against its member shares.
type Decision struct {
	Member           bool      `json:"member"`
	Allowed          bool      `json:"allowed"`
	Inactive         bool      `json:"inactive,omitempty"`
	Pool             string    `json:"pool,omitempty"`
	Name             string    `json:"name,omitempty"`
	Window           string    `json:"window,omitempty"`
	AllocatedUSD     float64   `json:"allocated_usd"`
	UsedUSD          float64   `json:"used_usd"`
	RemainingUSD     float64   `json:"remaining_usd"`
	RemainingPercent float64   `json:"remaining_percent"`
	ResetAt          time.Time `json:"reset_at"`
}

// RetryAfterSeconds is the whole seconds until the pool window resets, at least one.
func (d Decision) RetryAfterSeconds(now time.Time) int {
	secs := int(math.Ceil(d.ResetAt.Sub(now).Seconds()))
	if secs < 1 {
		return 1
	}
	return secs
}

// Check decides whether apiKey may issue a request. A key that is in no pool, or whose pool
// has no active capacity yet, is never blocked. The first exhausted window blocks the key.
func (e *Engine) Check(apiKey string) Decision {
	if e == nil || strings.TrimSpace(apiKey) == "" {
		return Decision{Allowed: true}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled {
		return Decision{Allowed: true}
	}
	now := e.now()
	keyID := KeyID(apiKey)
	result := Decision{Allowed: true}
	tightest := math.Inf(1)
	for _, pool := range e.cfg.Pools {
		ps := e.pools[pool.Name]
		if ps == nil {
			continue
		}
		var member *memberState
		var share float64
		var name string
		for _, m := range pool.Members {
			if memberID(m) == keyID {
				member, share, name = ps.Members[keyID], m.Percent, m.Name
				break
			}
		}
		if member == nil {
			continue
		}
		e.rollPoolLocked(pool, ps, now)
		for _, window := range []string{WindowFiveHour, WindowWeekly} {
			total := e.poolTotalLocked(pool.Name, window, now)
			d := Decision{Member: true, Allowed: true, Pool: pool.Name, Name: name, Window: window, ResetAt: ps.window(window).ResetAt}
			if total <= 0 {
				d.Inactive = true
				if !result.Member {
					result = d
				}
				continue
			}
			d.AllocatedUSD = total * share / 100
			d.UsedUSD = member.FiveHour.UsedUSD
			if window == WindowWeekly {
				d.UsedUSD = member.Weekly.UsedUSD
			}
			d.RemainingUSD = math.Max(0, d.AllocatedUSD-d.UsedUSD)
			if d.AllocatedUSD > 0 {
				d.RemainingPercent = 100 * d.RemainingUSD / d.AllocatedUSD
			}
			if d.UsedUSD >= d.AllocatedUSD {
				d.Allowed = false
				return d
			}
			if frac := d.RemainingPercent; frac < tightest {
				tightest = frac
				result = d
			}
		}
	}
	return result
}

// RemainingFraction reports how much of an account's tightest window is still free, for
// routing. ok is false when the account belongs to no pool or is currently excluded.
func (e *Engine) RemainingFraction(authID string) (float64, bool) {
	if e == nil {
		return 0, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled {
		return 0, false
	}
	now := e.now()
	for _, pool := range e.cfg.Pools {
		for _, pa := range e.resolvePool(pool, now) {
			if pa.info == nil || pa.info.ID != authID {
				continue
			}
			if !pa.included {
				return 0, false
			}
			used := 0.0
			for _, window := range []string{WindowFiveHour, WindowWeekly} {
				if u := e.accountUtilization(pa, window, now); u > used {
					used = u
				}
			}
			return math.Max(0, 1-used), true
		}
	}
	return 0, false
}

// accountUtilization is the observed utilization of an unexpired window, else the cost-based estimate.
func (e *Engine) accountUtilization(pa poolAccount, window string, now time.Time) float64 {
	if pa.state == nil {
		return 0
	}
	w := pa.state.window(window)
	if !w.ResetAt.IsZero() && !now.Before(w.ResetAt) {
		return 0
	}
	if w.UtilSeen {
		return w.Utilization
	}
	limit, _, _ := e.effectiveLimit(pa, window)
	if limit <= 0 {
		return 0
	}
	return sanitizeFloat(w.CostUSD / limit)
}

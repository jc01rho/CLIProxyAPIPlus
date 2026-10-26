package teamshare

import (
	"math"
	"sort"
	"time"
)

// WindowView is one window of an account or a member in the management response.
type WindowView struct {
	Percent  *float64   `json:"percent,omitempty"`
	ResetAt  *time.Time `json:"reset_at,omitempty"`
	CostUSD  float64    `json:"window_cost_usd"`
	LimitUSD float64    `json:"estimated_limit_usd"`
	Source   string     `json:"limit_source"`
	Samples  int        `json:"calibration_samples"`
}

// AccountView is one pool account in the management response.
type AccountView struct {
	AuthID       string                  `json:"auth_id"`
	Index        string                  `json:"auth_index,omitempty"`
	Email        string                  `json:"email,omitempty"`
	Ref          string                  `json:"ref"`
	Tier         string                  `json:"tier"`
	Status       string                  `json:"status"`
	Included     bool                    `json:"included_in_total"`
	ReauthReason string                  `json:"reauth_reason,omitempty"`
	Requests     int64                   `json:"requests"`
	TotalCostUSD float64                 `json:"total_cost_usd"`
	FiveHour     WindowView              `json:"five_hour"`
	Weekly       WindowView              `json:"weekly"`
	Models       map[string]*ModelTotals `json:"models,omitempty"`
}

// MemberWindowView is a member's share of one pool window.
type MemberWindowView struct {
	AllocatedUSD     float64 `json:"allocated_usd"`
	UsedUSD          float64 `json:"used_usd"`
	RemainingUSD     float64 `json:"remaining_usd"`
	RemainingPercent float64 `json:"remaining_percent"`
}

// MemberView is one pool member in the management response.
type MemberView struct {
	Name     string           `json:"name,omitempty"`
	KeyID    string           `json:"key_id"`
	Percent  float64          `json:"percent"`
	Requests int64            `json:"requests"`
	FiveHour MemberWindowView `json:"five_hour"`
	Weekly   MemberWindowView `json:"weekly"`
}

// PoolWindowView is a pool-wide window.
type PoolWindowView struct {
	TotalUSD  float64   `json:"total_usd"`
	ResetAt   time.Time `json:"reset_at"`
	SharedUSD float64   `json:"unassigned_used_usd"`
}

// PoolView is one pool in the management response.
type PoolView struct {
	Name               string         `json:"name"`
	Enforcement        string         `json:"enforcement"`
	FiveHour           PoolWindowView `json:"five_hour"`
	Weekly             PoolWindowView `json:"weekly"`
	UnassignedPercent  float64        `json:"unassigned_percent"`
	ActiveAccountCount int            `json:"active_accounts"`
	Accounts           []AccountView  `json:"accounts"`
	Members            []MemberView   `json:"members"`
}

// Snapshot is the full team-share state served by GET /v0/management/team-share.
type Snapshot struct {
	Enabled     bool       `json:"enabled"`
	GeneratedAt time.Time  `json:"generated_at"`
	Pools       []PoolView `json:"pools"`
}

// Snapshot returns the current state of every pool.
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	out := Snapshot{Enabled: e.cfg.Enabled, GeneratedAt: now.UTC(), Pools: []PoolView{}}
	for _, pool := range e.cfg.Pools {
		ps := e.pools[pool.Name]
		if ps == nil {
			continue
		}
		e.rollPoolLocked(pool, ps, now)
		view := PoolView{Name: pool.Name, Enforcement: "active", Accounts: []AccountView{}, Members: []MemberView{}}
		for _, pa := range e.resolvePool(pool, now) {
			view.Accounts = append(view.Accounts, e.accountView(pa, now))
			if pa.included {
				view.ActiveAccountCount++
			}
		}
		view.FiveHour = PoolWindowView{TotalUSD: sanitizeFloat(e.poolTotalLocked(pool.Name, WindowFiveHour, now)), ResetAt: ps.FiveHour.ResetAt.UTC(), SharedUSD: ps.FiveHour.SharedUSD}
		view.Weekly = PoolWindowView{TotalUSD: sanitizeFloat(e.poolTotalLocked(pool.Name, WindowWeekly, now)), ResetAt: ps.Weekly.ResetAt.UTC(), SharedUSD: ps.Weekly.SharedUSD}
		if view.FiveHour.TotalUSD <= 0 || view.Weekly.TotalUSD <= 0 {
			view.Enforcement = "inactive: no active accounts"
		}
		sum := 0.0
		for _, m := range pool.Members {
			sum += m.Percent
			ms := ps.Members[memberID(m)]
			if ms == nil {
				ms = &memberState{}
			}
			view.Members = append(view.Members, MemberView{
				Name: m.Name, KeyID: memberID(m), Percent: m.Percent, Requests: ms.Requests,
				FiveHour: memberWindowView(view.FiveHour.TotalUSD, m.Percent, ms.FiveHour.UsedUSD),
				Weekly:   memberWindowView(view.Weekly.TotalUSD, m.Percent, ms.Weekly.UsedUSD),
			})
		}
		view.UnassignedPercent = math.Max(0, 100-sum)
		out.Pools = append(out.Pools, view)
	}
	sort.SliceStable(out.Pools, func(i, j int) bool { return out.Pools[i].Name < out.Pools[j].Name })
	return out
}

func memberWindowView(total, percent, used float64) MemberWindowView {
	alloc := total * percent / 100
	v := MemberWindowView{AllocatedUSD: alloc, UsedUSD: used, RemainingUSD: math.Max(0, alloc-used)}
	if alloc > 0 {
		v.RemainingPercent = 100 * v.RemainingUSD / alloc
	}
	return v
}

func (e *Engine) accountView(pa poolAccount, now time.Time) AccountView {
	v := AccountView{Ref: pa.ref.Auth, Tier: pa.ref.Tier, Status: pa.status, Included: pa.included}
	if v.Tier == "" {
		v.Tier = "standard"
	}
	if pa.info != nil {
		v.AuthID, v.Index, v.Email = pa.info.ID, pa.info.Index, pa.info.Email
	}
	if pa.state == nil {
		v.FiveHour = e.windowView(pa, WindowFiveHour, now)
		v.Weekly = e.windowView(pa, WindowWeekly, now)
		return v
	}
	if v.Email == "" {
		v.Email = pa.state.Email
	}
	v.ReauthReason = pa.state.ReauthReason
	v.Requests = pa.state.Requests
	v.TotalCostUSD = pa.state.TotalCostUSD
	v.Models = make(map[string]*ModelTotals, len(pa.state.Models))
	for model, totals := range pa.state.Models {
		copied := *totals
		v.Models[model] = &copied
	}
	v.FiveHour = e.windowView(pa, WindowFiveHour, now)
	v.Weekly = e.windowView(pa, WindowWeekly, now)
	return v
}

func (e *Engine) windowView(pa poolAccount, window string, now time.Time) WindowView {
	limit, source, samples := e.effectiveLimit(pa, window)
	v := WindowView{LimitUSD: limit, Source: source, Samples: samples}
	if pa.state == nil {
		return v
	}
	w := pa.state.window(window)
	expired := !w.ResetAt.IsZero() && !now.Before(w.ResetAt)
	if !w.ResetAt.IsZero() {
		reset := w.ResetAt.UTC()
		v.ResetAt = &reset
	}
	if !expired {
		v.CostUSD = w.CostUSD
	}
	if util := e.accountUtilization(pa, window, now); w.UtilSeen || util > 0 {
		pct := math.Round(util*10000) / 100
		v.Percent = &pct
	}
	return v
}

package teamshare

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const (
	fiveHourWindow = 5 * time.Hour
	weeklyWindow   = 7 * 24 * time.Hour

	minCalibrationDelta = 0.03
	provisionalMinUtil  = 0.05
	ewmaAlpha           = 0.3
	planChangeFactor    = 3.0
	accountCacheTTL     = 5 * time.Second
)

// Window names used in snapshots and 429 bodies.
const (
	WindowFiveHour = "five_hour"
	WindowWeekly   = "weekly"
)

// Reasons an account is excluded from the pool total.
const (
	ReasonUnauthorized = "http_401"
	ReasonForbidden    = "http_403"
	ReasonPlanChanged  = "plan_changed"
)

// Default per-seat limit estimates in API-equivalent USD. They are rough
// placeholders for the first window and are replaced by measured calibration.
var defaultTierLimits = map[string]config.TeamShareTierLimit{
	config.TeamShareTierStandard: {FiveHourUSD: 6, WeeklyUSD: 120},
	config.TeamShareTierPremium:  {FiveHourUSD: 30, WeeklyUSD: 600},
}

// KeyID returns the stable short identifier of an API key for use as a member key-id.
func KeyID(apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return hex.EncodeToString(sum[:])[:12]
}

// AccountInfo is the runtime view of one credential supplied by the host.
type AccountInfo struct {
	ID       string
	Index    string
	Email    string
	FileName string
	Provider string
	Disabled bool
}

func (a AccountInfo) matches(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	for _, v := range []string{a.ID, a.Index, a.Email, a.FileName} {
		if v != "" && strings.EqualFold(v, ref) {
			return true
		}
	}
	return false
}

// ModelTotals accumulates tokens and API-equivalent cost for one model.
type ModelTotals struct {
	Requests   int64   `json:"requests"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	CostUSD    float64 `json:"cost_usd"`
}

type acctWindow struct {
	ResetAt     time.Time `json:"reset_at"`
	CostUSD     float64   `json:"cost_usd"`
	Utilization float64   `json:"utilization"`
	UtilSeen    bool      `json:"util_seen"`
	ObservedAt  time.Time `json:"observed_at"`
	Complete    bool      `json:"complete"`
	Anchored    bool      `json:"anchored"`
	AnchorUtil  float64   `json:"anchor_util"`
	AnchorCost  float64   `json:"anchor_cost"`
	CurrentEst  float64   `json:"current_estimate"`
	Calib       float64   `json:"calibration"`
	CalibN      int       `json:"calibration_samples"`
}

type accountState struct {
	ID           string                  `json:"id"`
	Index        string                  `json:"index,omitempty"`
	Email        string                  `json:"email,omitempty"`
	Requests     int64                   `json:"requests"`
	TotalCostUSD float64                 `json:"total_cost_usd"`
	Models       map[string]*ModelTotals `json:"models"`
	FiveHour     acctWindow              `json:"five_hour"`
	Weekly       acctWindow              `json:"weekly"`
	Reauth       bool                    `json:"reauth"`
	ReauthReason string                  `json:"reauth_reason,omitempty"`
	ReauthAt     time.Time               `json:"reauth_at,omitempty"`
	LastSeen     time.Time               `json:"last_seen,omitempty"`
}

func (a *accountState) window(name string) *acctWindow {
	if name == WindowWeekly {
		return &a.Weekly
	}
	return &a.FiveHour
}

type memberWindow struct {
	UsedUSD float64 `json:"used_usd"`
}

type memberState struct {
	FiveHour memberWindow `json:"five_hour"`
	Weekly   memberWindow `json:"weekly"`
	Requests int64        `json:"requests"`
	Total    float64      `json:"total_cost_usd"`
}

type poolWindow struct {
	ResetAt    time.Time `json:"reset_at"`
	SharedUSD  float64   `json:"shared_usd"`
	SharedReqs int64     `json:"shared_requests"`
}

type poolState struct {
	FiveHour poolWindow              `json:"five_hour"`
	Weekly   poolWindow              `json:"weekly"`
	Members  map[string]*memberState `json:"members"`
}

func (p *poolState) window(name string) *poolWindow {
	if name == WindowWeekly {
		return &p.Weekly
	}
	return &p.FiveHour
}

func windowDuration(name string) time.Duration {
	if name == WindowWeekly {
		return weeklyWindow
	}
	return fiveHourWindow
}

// Engine tracks per-account usage, derives the pool totals and enforces member shares.
type Engine struct {
	mu       sync.Mutex
	now      func() time.Time
	cfg      config.TeamShareConfig
	prices   *PriceTable
	lister   func() []AccountInfo
	accounts map[string]*accountState
	pools    map[string]*poolState

	cachedAccounts []AccountInfo
	cachedAt       time.Time

	statePath string
	dirty     bool
	lastFlush time.Time
	auditPath string
}

// NewEngine creates an engine. lister may be nil until the host wires it.
func NewEngine(lister func() []AccountInfo) *Engine {
	return &Engine{
		now:      time.Now,
		prices:   NewPriceTable(nil),
		lister:   lister,
		accounts: map[string]*accountState{},
		pools:    map[string]*poolState{},
	}
}

// SetClock replaces the time source (tests).
func (e *Engine) SetClock(now func() time.Time) {
	e.mu.Lock()
	e.now = now
	e.mu.Unlock()
}

// SetAccountLister wires the credential source.
func (e *Engine) SetAccountLister(lister func() []AccountInfo) {
	e.mu.Lock()
	e.lister = lister
	e.cachedAt = time.Time{}
	e.mu.Unlock()
}

// Enabled reports whether team-share is switched on.
func (e *Engine) Enabled() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Enabled
}

// Apply installs a validated configuration. An invalid one is rejected and the
// previous configuration stays active.
func (e *Engine) Apply(cfg config.TeamShareConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
	e.prices = NewPriceTable(cfg.Pricing)
	e.auditPath = cfg.AuditLog
	known := map[string]struct{}{}
	for _, pool := range cfg.Pools {
		known[pool.Name] = struct{}{}
		ps := e.pools[pool.Name]
		if ps == nil {
			ps = &poolState{Members: map[string]*memberState{}}
			e.pools[pool.Name] = ps
		}
		keep := map[string]struct{}{}
		for _, m := range pool.Members {
			id := memberID(m)
			keep[id] = struct{}{}
			if ps.Members[id] == nil {
				ps.Members[id] = &memberState{}
			}
		}
		for id := range ps.Members {
			if _, ok := keep[id]; !ok {
				delete(ps.Members, id)
			}
		}
	}
	for name := range e.pools {
		if _, ok := known[name]; !ok {
			delete(e.pools, name)
		}
	}
	e.dirty = true
	return nil
}

func memberID(m config.TeamShareMember) string {
	if id := strings.TrimSpace(m.KeyID); id != "" {
		return strings.ToLower(id)
	}
	return KeyID(m.APIKey)
}

func (e *Engine) accountList(now time.Time) []AccountInfo {
	if e.lister == nil {
		return nil
	}
	if !e.cachedAt.IsZero() && now.Sub(e.cachedAt) < accountCacheTTL {
		return e.cachedAccounts
	}
	e.cachedAccounts = e.lister()
	e.cachedAt = now
	return e.cachedAccounts
}

func (e *Engine) tierLimit(tier, window string) float64 {
	tier = config.NormalizeTeamShareTier(tier)
	limit, ok := e.cfg.Tiers[tier]
	if !ok || (limit.FiveHourUSD <= 0 && limit.WeeklyUSD <= 0) {
		limit = defaultTierLimits[tier]
	}
	if window == WindowWeekly {
		return limit.WeeklyUSD
	}
	return limit.FiveHourUSD
}

// poolAccount is a configured pool account resolved against the live credential list.
type poolAccount struct {
	ref      config.TeamShareAccount
	info     *AccountInfo
	state    *accountState
	included bool
	status   string
}

func (e *Engine) resolvePool(pool config.TeamSharePool, now time.Time) []poolAccount {
	infos := e.accountList(now)
	out := make([]poolAccount, 0, len(pool.Accounts))
	for _, ref := range pool.Accounts {
		pa := poolAccount{ref: ref}
		for i := range infos {
			if infos[i].matches(ref.Auth) {
				pa.info = &infos[i]
				break
			}
		}
		switch {
		case pa.info == nil:
			pa.status = "missing"
		default:
			pa.state = e.accounts[pa.info.ID]
			switch {
			case pa.info.Disabled:
				pa.status = "disabled"
			case pa.state != nil && pa.state.Reauth:
				pa.status = "reauth_required"
			default:
				pa.status = "active"
				pa.included = true
			}
		}
		out = append(out, pa)
	}
	return out
}

func (e *Engine) account(id string, create bool) *accountState {
	st := e.accounts[id]
	if st == nil && create {
		st = &accountState{ID: id, Models: map[string]*ModelTotals{}}
		e.accounts[id] = st
	}
	return st
}

// effectiveLimit returns the limit estimate for one account window and where it came from.
func (e *Engine) effectiveLimit(pa poolAccount, window string) (usd float64, source string, samples int) {
	if pa.state != nil {
		w := pa.state.window(window)
		if w.CalibN > 0 && w.Calib > 0 {
			return w.Calib, "calibrated", w.CalibN
		}
		if w.CurrentEst > 0 {
			return w.CurrentEst, "provisional", 0
		}
	}
	return e.tierLimit(pa.ref.Tier, window), "tier-default", 0
}

// PoolTotalUSD returns the total API-equivalent capacity of a pool window over its active accounts.
func (e *Engine) PoolTotalUSD(poolName, window string) float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.poolTotalLocked(poolName, window, e.now())
}

func (e *Engine) poolTotalLocked(poolName, window string, now time.Time) float64 {
	for _, pool := range e.cfg.Pools {
		if pool.Name != poolName {
			continue
		}
		total := 0.0
		for _, pa := range e.resolvePool(pool, now) {
			if !pa.included {
				continue
			}
			limit, _, _ := e.effectiveLimit(pa, window)
			total += limit
		}
		return total
	}
	return 0
}

func sanitizeFloat(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

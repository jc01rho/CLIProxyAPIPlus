package teamshare

import (
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type fixture struct {
	t      *testing.T
	e      *Engine
	now    time.Time
	infos  []AccountInfo
	keyA   string
	keyB   string
	pool   config.TeamSharePool
	config config.TeamShareConfig
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		t:    t,
		now:  time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC),
		keyA: "sk-member-a",
		keyB: "sk-member-b",
		infos: []AccountInfo{
			{ID: "acc-1", Index: "i1", Email: "one@junghap.top", Provider: "claude"},
			{ID: "acc-2", Index: "i2", Email: "two@junghap.top", Provider: "claude"},
		},
	}
	f.e = NewEngine(func() []AccountInfo { return append([]AccountInfo(nil), f.infos...) })
	f.e.SetClock(func() time.Time { return f.now })
	f.config = config.TeamShareConfig{
		Enabled: true,
		Pools: []config.TeamSharePool{{
			Name:     "team",
			Accounts: []config.TeamShareAccount{{Auth: "one@junghap.top", Tier: "standard"}, {Auth: "i2", Tier: "standard"}},
			Members: []config.TeamShareMember{
				{Name: "a", APIKey: f.keyA, Percent: 60},
				{Name: "b", KeyID: KeyID(f.keyB), Percent: 40},
			},
		}},
	}
	if err := f.e.Apply(f.config); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) record(auth, key string, outputTokens int64, h http.Header) {
	f.t.Helper()
	f.e.Record(Sample{AuthID: auth, APIKey: key, Model: "claude-sonnet-5-5", Usage: Usage{Output: outputTokens}, Headers: h, At: f.now})
}

func rateHeaders(util5h float64, reset5h time.Time, util7d float64, reset7d time.Time) http.Header {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", strconv.FormatFloat(util5h, 'f', 4, 64))
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(reset5h.Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", strconv.FormatFloat(util7d, 'f', 4, 64))
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(reset7d.Unix(), 10))
	return h
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func TestCostConversionUsesPublicPricesAndOverrides(t *testing.T) {
	table := NewPriceTable(nil)
	u := Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000}
	near(t, "sonnet", table.CostUSD("claude-sonnet-4-5", u), 3+15+0.3+3.75)
	near(t, "opus 4.1", table.CostUSD("claude-opus-4-1-20250805", u), 15+75+1.5+18.75)
	near(t, "opus new", table.CostUSD("claude-opus-5-5", u), 5+25+0.5+6.25)
	near(t, "haiku", table.CostUSD("claude-haiku-4-5", u), 1+5+0.1+1.25)
	near(t, "unknown falls back to sonnet", table.CostUSD("mystery", u), 3+15+0.3+3.75)

	override := NewPriceTable(map[string]config.TeamSharePrice{"sonnet": {Input: 10, Output: 20}})
	near(t, "override beats builtin", override.CostUSD("claude-sonnet-4-5", u), 10+20+1+12.5)
}

func TestPoolTotalIsActiveAccountsTimesTierLimit(t *testing.T) {
	f := newFixture(t)
	near(t, "two standard seats", f.e.PoolTotalUSD("team", WindowFiveHour), 12)
	near(t, "weekly", f.e.PoolTotalUSD("team", WindowWeekly), 240)

	f.config.Pools[0].Accounts[1].Tier = "premium"
	if err := f.e.Apply(f.config); err != nil {
		t.Fatal(err)
	}
	near(t, "standard + premium", f.e.PoolTotalUSD("team", WindowFiveHour), 6+30)

	f.config.Tiers = map[string]config.TeamShareTierLimit{"standard": {FiveHourUSD: 10, WeeklyUSD: 100}}
	if err := f.e.Apply(f.config); err != nil {
		t.Fatal(err)
	}
	near(t, "config tier override", f.e.PoolTotalUSD("team", WindowFiveHour), 10+30)
}

func TestExcludingAnAccountRecomputesTotal(t *testing.T) {
	f := newFixture(t)
	near(t, "before", f.e.PoolTotalUSD("team", WindowFiveHour), 12)

	f.infos[1].Disabled = true
	f.e.SetAccountLister(func() []AccountInfo { return append([]AccountInfo(nil), f.infos...) })
	near(t, "disabled seat leaves the total", f.e.PoolTotalUSD("team", WindowFiveHour), 6)

	f.infos[1].Disabled = false
	f.e.SetAccountLister(func() []AccountInfo { return append([]AccountInfo(nil), f.infos...) })
	near(t, "restored", f.e.PoolTotalUSD("team", WindowFiveHour), 12)

	f.e.Record(Sample{AuthID: "acc-2", Failed: true, StatusCode: http.StatusUnauthorized, At: f.now})
	near(t, "401 marks re-auth and leaves the total", f.e.PoolTotalUSD("team", WindowFiveHour), 6)
	snap := f.e.Snapshot()
	var status string
	for _, acc := range snap.Pools[0].Accounts {
		if acc.AuthID == "acc-2" {
			status = acc.Status + "/" + acc.ReauthReason
		}
	}
	if status != "reauth_required/http_401" {
		t.Fatalf("account status = %q", status)
	}

	f.e.Record(Sample{AuthID: "acc-2", APIKey: f.keyA, Model: "claude-sonnet-5-5", Usage: Usage{Output: 10}, At: f.now})
	near(t, "a later success clears the 401 flag", f.e.PoolTotalUSD("team", WindowFiveHour), 12)
}

func TestMemberOverQuotaIsRejectedWithRemainingShare(t *testing.T) {
	f := newFixture(t)
	// total 12 USD per 5h: a=60% -> 7.2, b=40% -> 4.8. 100k sonnet output tokens = 1.5 USD.
	for i := 0; i < 4; i++ {
		f.record("acc-1", f.keyA, 100_000, nil)
	}
	d := f.e.Check(f.keyA)
	if !d.Allowed {
		t.Fatalf("a at 6.0 of 7.2 must be allowed: %+v", d)
	}
	near(t, "allocated", d.AllocatedUSD, 7.2)
	near(t, "used", d.UsedUSD, 6)
	near(t, "remaining", d.RemainingUSD, 1.2)

	f.record("acc-2", f.keyA, 100_000, nil)
	d = f.e.Check(f.keyA)
	if d.Allowed || d.Window != WindowFiveHour {
		t.Fatalf("a at 7.5 of 7.2 must be rejected on the 5h window: %+v", d)
	}
	near(t, "rejected remaining", d.RemainingUSD, 0)
	if secs := d.RetryAfterSeconds(f.now); secs < 1 || secs > int((5*time.Hour).Seconds()) {
		t.Fatalf("retry-after %d outside the window", secs)
	}
	if !f.e.Check(f.keyB).Allowed {
		t.Fatal("b has used nothing and must stay allowed")
	}
	if !f.e.Check("sk-unlisted").Allowed {
		t.Fatal("a key outside every pool is never limited")
	}

	f.now = f.now.Add(5*time.Hour + time.Minute)
	if !f.e.Check(f.keyA).Allowed {
		t.Fatal("a must be allowed again after the pool window reset")
	}
}

func TestWeeklyWindowAlsoEnforced(t *testing.T) {
	f := newFixture(t)
	f.config.Tiers = map[string]config.TeamShareTierLimit{"standard": {FiveHourUSD: 1000, WeeklyUSD: 10}}
	if err := f.e.Apply(f.config); err != nil {
		t.Fatal(err)
	}
	// weekly total 20 -> b (40%) gets 8 USD.
	for i := 0; i < 6; i++ {
		f.record("acc-1", f.keyB, 100_000, nil)
	}
	d := f.e.Check(f.keyB)
	if d.Allowed || d.Window != WindowWeekly {
		t.Fatalf("b must be rejected on the weekly window: %+v", d)
	}
}

func TestUnassignedUsageIsTrackedSeparately(t *testing.T) {
	f := newFixture(t)
	f.record("acc-1", "sk-someone-else", 100_000, nil)
	f.record("acc-1", "", 100_000, nil)
	snap := f.e.Snapshot()
	near(t, "shared usage", snap.Pools[0].FiveHour.SharedUSD, 3)
	for _, m := range snap.Pools[0].Members {
		near(t, "member used "+m.Name, m.FiveHour.UsedUSD, 0)
	}
}

func TestCalibrationReplacesTierDefaultFromObservedUtilization(t *testing.T) {
	f := newFixture(t)
	reset5h := f.now.Add(2 * time.Hour)
	reset7d := f.now.Add(48 * time.Hour)
	// First observation anchors; cost 1.5 USD moves utilization 0.10 -> 0.40 => limit ~5 USD.
	f.record("acc-1", f.keyA, 100_000, rateHeaders(0.10, reset5h, 0.01, reset7d))
	f.record("acc-1", f.keyA, 100_000, rateHeaders(0.40, reset5h, 0.02, reset7d))
	snap := f.e.Snapshot()
	var acc AccountView
	for _, a := range snap.Pools[0].Accounts {
		if a.AuthID == "acc-1" {
			acc = a
		}
	}
	if acc.FiveHour.Source != "provisional" {
		t.Fatalf("source = %q, want provisional", acc.FiveHour.Source)
	}
	near(t, "provisional 5h limit", acc.FiveHour.LimitUSD, 1.5/0.30)
	if acc.FiveHour.Percent == nil || math.Abs(*acc.FiveHour.Percent-40) > 1e-6 {
		t.Fatalf("5h percent = %v", acc.FiveHour.Percent)
	}

	// Window rollover folds the estimate into the moving average.
	f.now = reset5h.Add(time.Minute)
	nextReset := f.now.Add(5 * time.Hour)
	f.record("acc-1", f.keyA, 50_000, rateHeaders(0.15, nextReset, 0.03, reset7d))
	near(t, "calibrated 5h limit", f.e.accounts["acc-1"].FiveHour.Calib, 1.5/0.30)
	if f.e.accounts["acc-1"].FiveHour.CalibN != 1 {
		t.Fatalf("calibration samples = %d", f.e.accounts["acc-1"].FiveHour.CalibN)
	}
	// The new window started fully tracked: 0.75 USD at 15% => 5 USD again.
	f.record("acc-1", f.keyA, 50_000, rateHeaders(0.30, nextReset, 0.04, reset7d))
	near(t, "second window estimate", f.e.accounts["acc-1"].FiveHour.CurrentEst, 1.5/0.30)
}

func TestPlanChangeMarksAccountReauthAndExcludesIt(t *testing.T) {
	f := newFixture(t)
	acc := f.e.account("acc-1", true)
	acc.FiveHour.Calib, acc.FiveHour.CalibN = 5, 3
	acc.FiveHour.Anchored, acc.FiveHour.AnchorUtil, acc.FiveHour.AnchorCost = true, 0, 0
	reset := f.now.Add(time.Hour)
	// 1.5 USD at 5% utilization => 30 USD limit, 6x the calibrated 5.
	f.record("acc-1", f.keyA, 100_000, rateHeaders(0.05, reset, 0.01, f.now.Add(24*time.Hour)))
	if !acc.Reauth || acc.ReauthReason != ReasonPlanChanged {
		t.Fatalf("reauth=%v reason=%q", acc.Reauth, acc.ReauthReason)
	}
	near(t, "total without the changed account", f.e.PoolTotalUSD("team", WindowFiveHour), 6)
	if !f.e.AckReauth("acc-1") {
		t.Fatal("ack failed")
	}
	near(t, "total after ack", f.e.PoolTotalUSD("team", WindowFiveHour), 12)
}

func TestRemainingFractionPrefersTheEmptierAccount(t *testing.T) {
	f := newFixture(t)
	reset := f.now.Add(2 * time.Hour)
	f.record("acc-1", f.keyA, 10_000, rateHeaders(0.80, reset, 0.10, f.now.Add(48*time.Hour)))
	f.record("acc-2", f.keyB, 10_000, rateHeaders(0.20, reset, 0.10, f.now.Add(48*time.Hour)))
	r1, ok1 := f.e.RemainingFraction("acc-1")
	r2, ok2 := f.e.RemainingFraction("acc-2")
	if !ok1 || !ok2 || r2 <= r1 {
		t.Fatalf("remaining acc-1=%v acc-2=%v", r1, r2)
	}
	near(t, "acc-1 remaining", r1, 0.2)
	if _, ok := f.e.RemainingFraction("outsider"); ok {
		t.Fatal("an account outside every pool must not report a remaining fraction")
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "state.json")
	f.e.SetStatePath(path)
	f.record("acc-1", f.keyA, 100_000, nil)
	if err := f.e.Flush(); err != nil {
		t.Fatal(err)
	}

	again := NewEngine(func() []AccountInfo { return f.infos })
	again.SetClock(func() time.Time { return f.now })
	if err := again.Apply(f.config); err != nil {
		t.Fatal(err)
	}
	again.SetStatePath(path)
	if err := again.Load(); err != nil {
		t.Fatal(err)
	}
	d := again.Check(f.keyA)
	near(t, "restored member usage", d.UsedUSD, 1.5)
	near(t, "restored account tokens", float64(again.accounts["acc-1"].Models["claude-sonnet-5-5"].Output), 100_000)
}

func TestApplyRejectsInvalidPercentAndKeepsPreviousConfig(t *testing.T) {
	f := newFixture(t)
	bad := f.config
	bad.Pools = []config.TeamSharePool{{
		Name:     "team",
		Accounts: []config.TeamShareAccount{{Auth: "i1"}},
		Members:  []config.TeamShareMember{{APIKey: "x", Percent: 70}, {APIKey: "y", Percent: 40}},
	}}
	if err := f.e.Apply(bad); err == nil {
		t.Fatal("percent sum 110 must be rejected")
	}
	near(t, "previous config still active", f.e.PoolTotalUSD("team", WindowFiveHour), 12)
}

func TestParseObservationReadsClaudeUnifiedHeaders(t *testing.T) {
	reset := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	obs := ParseObservation(rateHeaders(0.42, reset, 0.07, reset.Add(72*time.Hour)))
	if !obs.FiveHour.HasUtil || obs.FiveHour.Utilization != 0.42 || !obs.FiveHour.Reset.Equal(reset) {
		t.Fatalf("five hour: %+v", obs.FiveHour)
	}
	if obs.Weekly.Utilization != 0.07 {
		t.Fatalf("weekly: %+v", obs.Weekly)
	}
	if ParseObservation(http.Header{}).Any() {
		t.Fatal("empty headers carry no observation")
	}
}

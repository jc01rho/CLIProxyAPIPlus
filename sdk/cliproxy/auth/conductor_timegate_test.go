package auth

import (
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func testTimeGateRules() []internalconfig.ModelTimeGate {
	return []internalconfig.ModelTimeGate{
		{
			Name:     "deepseek-peek-time",
			Schedule: "0 1 * * 1-5",
			Duration: "3h",
			Provider: "deepseek",
			AuthID:   "deep-free-1",
			Models:   []string{"deepseek-*"},
		},
	}
}

// TestModelTimeGateWindow verifies UTC window boundaries. Schedule "0 1 * *
// 1-5" with 3h duration covers Monday 01:00-04:00 UTC.
func TestModelTimeGateWindow(t *testing.T) {
	// Monday 2026-09-14.
	inWindow := time.Date(2026, 9, 14, 2, 30, 0, 0, time.UTC)
	if !modelTimeGateActiveAt("0 1 * * 1-5", "3h", inWindow) {
		t.Error("Monday 02:30 UTC should be inside the gate")
	}
	before := time.Date(2026, 9, 14, 0, 59, 0, 0, time.UTC)
	if modelTimeGateActiveAt("0 1 * * 1-5", "3h", before) {
		t.Error("Monday 00:59 UTC should be outside the gate")
	}
	after := time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC)
	if modelTimeGateActiveAt("0 1 * * 1-5", "3h", after) {
		t.Error("window end is exclusive: Monday 04:00 UTC should be outside")
	}
	// Saturday is not in 1-5.
	sat := time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)
	if modelTimeGateActiveAt("0 1 * * 1-5", "3h", sat) {
		t.Error("Saturday should be outside the Mon-Fri gate")
	}
	// Malformed schedule or duration never matches.
	if modelTimeGateActiveAt("not-a-cron", "3h", inWindow) {
		t.Error("malformed schedule must not match")
	}
	if modelTimeGateActiveAt("0 1 * * 1-5", "bogus", inWindow) {
		t.Error("malformed duration must not match")
	}
}

// TestModelTimeGateMatching verifies the provider/auth/model axes.
func TestModelTimeGateMatching(t *testing.T) {
	oldNow := modelTimeGateNow
	modelTimeGateNow = func() time.Time {
		return time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC) // Monday, in window
	}
	defer func() { modelTimeGateNow = oldNow }()

	rules := testTimeGateRules()
	mkAuth := func(provider, id string) *Auth {
		return &Auth{ID: id, Provider: provider}
	}

	if _, gated := modelTimeGatedByConfig(nil, rules, mkAuth("deepseek", "deep-free-1"), "deepseek-v3.2"); !gated {
		t.Error("matching provider/auth/model should be gated")
	}
	if _, gated := modelTimeGatedByConfig(nil, rules, mkAuth("other-provider", "op-1"), "deepseek-v3.2"); gated {
		t.Error("a different provider serving the same model must NOT be gated")
	}
	if _, gated := modelTimeGatedByConfig(nil, rules, mkAuth("deepseek", "deep-paid-2"), "deepseek-v3.2"); gated {
		t.Error("non-matching auth ID must NOT be gated")
	}
	if _, gated := modelTimeGatedByConfig(nil, rules, mkAuth("deepseek", "deep-free-1"), "claude-opus"); gated {
		t.Error("non-matching model must NOT be gated")
	}
	// Outside the window nothing matches.
	modelTimeGateNow = func() time.Time {
		return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	}
	if _, gated := modelTimeGatedByConfig(nil, rules, mkAuth("deepseek", "deep-free-1"), "deepseek-v3.2"); gated {
		t.Error("outside the window nothing should be gated")
	}
}

// TestModelTimeGateMatchesConfigAlias proves a rule written against the real
// upstream model name (e.g. "*deepseek-v4.1-flash*") also gates a request
// that used a client-side config alias for the same model (e.g.
// "command-deepseek41-flash"), reproducing the reported production case: the
// registry alone cannot resolve this because registry.ModelInfo.ID stores
// only the alias (see buildConfiguredModelInfo) with no reverse mapping back
// to the real name, so the live per-provider config must be consulted.
func TestModelTimeGateMatchesConfigAlias(t *testing.T) {
	oldNow := modelTimeGateNow
	modelTimeGateNow = func() time.Time {
		return time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC) // Monday, in window
	}
	defer func() { modelTimeGateNow = oldNow }()

	rules := []internalconfig.ModelTimeGate{
		{
			Name:     "deepseek-peek-time",
			Schedule: "0 1 * * 1-5",
			Duration: "3h",
			// No Provider/AuthID restriction, matching the reported config.
			Models: []string{"*deepseek-v4.1-flash*"},
		},
	}
	cfg := &internalconfig.Config{
		CommandCodeKey: []internalconfig.CommandCodeKey{
			{
				Models: []internalconfig.CommandCodeModel{
					{Name: "deepseek-v4.1-flash", Alias: "command-deepseek41-flash"},
				},
			},
		},
	}
	auth := &Auth{ID: "commandcode-apikey", Provider: "commandcode"}

	if _, gated := modelTimeGatedByConfig(cfg, rules, auth, "command-deepseek41-flash"); !gated {
		t.Error("request using the config alias must be gated by a rule written against the real model name")
	}
	if _, gated := modelTimeGatedByConfig(cfg, rules, auth, "deepseek-v4.1-flash"); !gated {
		t.Error("request using the real model name directly must still be gated")
	}
	if _, gated := modelTimeGatedByConfig(nil, rules, auth, "command-deepseek41-flash"); gated {
		t.Error("without config the alias must not resolve to the real model name")
	}
	if _, gated := modelTimeGatedByConfig(cfg, rules, auth, "some-other-alias"); gated {
		t.Error("an unrelated alias must not be gated")
	}
}

// TestParseCronStart verifies cron field parsing.
func TestParseCronStart(t *testing.T) {
	minute, hour, weekdays, ok := parseCronStart("0 1 * * 1-5")
	if !ok || minute != 0 || hour != 1 || len(weekdays) != 5 {
		t.Errorf("parse = %d %d %v %v", minute, hour, weekdays, ok)
	}
	if _, _, _, ok := parseCronStart("0 1 * *"); ok {
		t.Error("4-field cron must be rejected")
	}
	if _, _, _, ok := parseCronStart("0 1 15 * *"); ok {
		t.Error("day-of-month schedules are unsupported and must be rejected")
	}
	if _, _, _, ok := parseCronStart("*/5 1 * * *"); ok {
		t.Error("step minutes are unsupported and must be rejected")
	}
	if _, _, wd, ok := parseCronStart("0 0 * * 7"); !ok || len(wd) != 1 || wd[0] != 0 {
		t.Errorf("Sunday as 7 should normalize to 0: %v %v", wd, ok)
	}
	if _, _, wd, ok := parseCronStart("0 0 * * */2"); !ok || len(wd) != 4 {
		t.Errorf("step weekdays should expand: %v %v", wd, ok)
	}
}

// TestMatchGlob verifies wildcard matching.
func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"deepseek-*", "deepseek-v3.2", true},
		{"deepseek-*", "claude-opus", false},
		{"*", "anything", true},
		{"glm-?.?", "glm-5.3", true},
		{"glm-?.?", "glm-53", false},
	}
	for _, tc := range cases {
		got, err := matchGlob(tc.pattern, tc.value)
		if err != nil || got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, %v; want %v", tc.pattern, tc.value, got, err, tc.want)
		}
	}
}

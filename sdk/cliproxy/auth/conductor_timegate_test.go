package auth

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
				APIKey: "commandcode-test-key",
				Models: []internalconfig.CommandCodeModel{
					{Name: "deepseek-v4.1-flash", Alias: "command-deepseek41-flash"},
				},
			},
		},
	}
	auth := &Auth{
		ID:         "commandcode-apikey",
		Provider:   "commandcode",
		Attributes: map[string]string{AttributeAPIKey: "commandcode-test-key"},
	}

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

func TestModelTimeGatePreservesUngatedModelInSharedAliasPool(t *testing.T) {
	oldNow := modelTimeGateNow
	modelTimeGateNow = func() time.Time {
		return time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC)
	}
	defer func() { modelTimeGateNow = oldNow }()

	const (
		aliasModel   = "higher-coding"
		gatedModel   = "deepseek-v4.1-flash"
		allowedModel = "gemini-3-pro-preview"
		apiKey       = "test-gemini-key"
	)
	cfg := &internalconfig.Config{
		GeminiKey: []internalconfig.GeminiKey{{
			APIKey: apiKey,
			Models: []internalconfig.GeminiModel{
				{Name: gatedModel, Alias: aliasModel},
				{Name: allowedModel, Alias: aliasModel},
			},
		}},
		Routing: internalconfig.RoutingConfig{
			ModelTimeGates: []internalconfig.ModelTimeGate{{
				Name:     "deepseek-peek-time",
				Schedule: "0 1 * * 1-5",
				Duration: "3h",
				Models:   []string{"*deepseek-v4.1-flash*"},
			}},
		},
	}
	auth := &Auth{
		ID:         "gemini-key",
		Provider:   "gemini",
		Attributes: map[string]string{AttributeAPIKey: apiKey},
	}
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetConfig(cfg)
	executor := &aliasModelPoolExecutor{provider: "gemini", failFor: map[string]error{}}
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: aliasModel}, {ID: gatedModel}, {ID: allowedModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, gated := manager.authMatchesTimeGate(auth, aliasModel, cliproxyexecutor.Options{}); gated {
		t.Fatal("an alias with an allowed upstream model must remain eligible")
	}
	models, pooled := manager.preparedExecutionModels(auth, aliasModel)
	if !pooled {
		t.Fatal("shared alias should be treated as a model pool")
	}
	if len(models) != 1 || models[0] != allowedModel {
		t.Fatalf("execution models = %v, want [%s]", models, allowedModel)
	}
	resp, errExecute := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute shared alias pool: %v", errExecute)
	}
	if got, want := string(resp.Payload), auth.ID+":"+allowedModel; got != want {
		t.Fatalf("response payload = %q, want %q", got, want)
	}
}

func TestPickNextMixedMarksTimeGateExcludedAuthNotFound(t *testing.T) {
	oldNow := modelTimeGateNow
	modelTimeGateNow = func() time.Time {
		return time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC)
	}
	defer func() { modelTimeGateNow = oldNow }()

	const model = "deepseek-v4.1-flash"
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "gate-provider"})
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			ModelTimeGates: []internalconfig.ModelTimeGate{{
				Name:     "deepseek-peek-time",
				Schedule: "0 1 * * 1-5",
				Duration: "3h",
				Models:   []string{"*deepseek-v4.1-flash*"},
			}},
		},
	})
	auth := &Auth{ID: "gated-auth", Provider: "gate-provider", Status: StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	_, _, _, errPick := manager.pickNextMixedLegacy(context.Background(), []string{"gate-provider"}, model, cliproxyexecutor.Options{}, nil)
	if errPick == nil || errPick.Error() != "auth_not_found: no auth available" {
		t.Fatalf("pick error = %v, want auth_not_found", errPick)
	}
	if !errorHasTimeGateExclusion(errPick) {
		t.Fatal("auth_not_found must retain the time gate exclusion cause")
	}
}

// TestModelTimeGateAllowModeWhitelistsModels proves Mode:"allow" inverts the
// gate's polarity: within the active window, requests for a whitelisted
// model pass through untouched while every other model in scope is blocked;
// outside the window nothing is affected.
func TestModelTimeGateAllowModeWhitelistsModels(t *testing.T) {
	oldNow := modelTimeGateNow
	defer func() { modelTimeGateNow = oldNow }()

	rules := []internalconfig.ModelTimeGate{
		{
			Name:     "whitelist-window",
			Schedule: "0 1 * * 1-5",
			Duration: "3h",
			Mode:     "allow",
			Models:   []string{"deepseek-v3.2"},
		},
	}
	auth := &Auth{ID: "any-auth", Provider: "any-provider"}

	// Inside the window.
	modelTimeGateNow = func() time.Time { return time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC) }
	if _, gated := modelTimeGatedByConfig(nil, rules, auth, "deepseek-v3.2"); gated {
		t.Error("whitelisted model must pass through during the allow window")
	}
	if _, gated := modelTimeGatedByConfig(nil, rules, auth, "gpt-5.5"); !gated {
		t.Error("non-whitelisted model must be blocked during the allow window")
	}

	// Outside the window nothing is gated, whitelist or not.
	modelTimeGateNow = func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
	if _, gated := modelTimeGatedByConfig(nil, rules, auth, "gpt-5.5"); gated {
		t.Error("outside the window the allow rule must not block anything")
	}
}

// TestModelTimeGateAllowModeEmptyModelsBlocksEverything proves an allow-mode
// rule with no Models entries blocks every candidate in scope for the whole
// window (an empty allowlist grants no exceptions), matching the documented
// ModelTimeGate.Models contract.
func TestModelTimeGateAllowModeEmptyModelsBlocksEverything(t *testing.T) {
	oldNow := modelTimeGateNow
	modelTimeGateNow = func() time.Time { return time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC) }
	defer func() { modelTimeGateNow = oldNow }()

	rules := []internalconfig.ModelTimeGate{
		{Name: "lockdown", Schedule: "0 1 * * 1-5", Duration: "3h", Mode: "allow"},
	}
	auth := &Auth{ID: "any-auth", Provider: "any-provider"}
	if _, gated := modelTimeGatedByConfig(nil, rules, auth, "anything-at-all"); !gated {
		t.Error("an allow rule with an empty allowlist must block every model")
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

// TestModelTimeGateEnforcedOnMixedFastPath reproduces the reported production
// bypass: a mixed-provider request served through the scheduler fast path
// (pickNextMixed) must honor an active exclude gate instead of silently
// selecting the gated credential.
func TestModelTimeGateEnforcedOnMixedFastPath(t *testing.T) {
	oldNow := modelTimeGateNow
	modelTimeGateNow = func() time.Time {
		return time.Date(2026, 9, 14, 3, 17, 0, 0, time.UTC) // Monday, in 01:00-04:00 UTC window
	}
	defer func() { modelTimeGateNow = oldNow }()

	manager := NewManager(nil, nil, nil)
	manager.executors["ollama-openaicompatible"] = schedulerTestExecutor{}
	for _, auth := range []*Auth{
		{ID: "gated-auth", Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "ollama-openaicompatible"}},
		{ID: "open-auth", Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "ollama-openaicompatible"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	registerSchedulerModels(t, "ollama-openaicompatible", "deepseek-v4.1-flash", "gated-auth", "open-auth")
	manager.runtimeConfig.Store(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			ModelTimeGates: []internalconfig.ModelTimeGate{
				{
					Name:     "deepseek-peek-time-1",
					Schedule: "0 1 * * 1-5",
					Duration: "3h",
					AuthID:   "gated-auth",
					Models:   []string{"*deepseek-v4.1-flash*"},
				},
			},
		},
	})

	if !manager.useSchedulerFastPath() {
		t.Skip("scheduler fast path unavailable without built-in selector state")
	}
	selected, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"ollama-openaicompatible"}, "deepseek-v4.1-flash", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if selected == nil {
		t.Fatal("pickNextMixed() returned no auth")
	}
	if selected.ID != "open-auth" {
		t.Fatalf("pickNextMixed() selected %q, want open-auth (gated-auth must be excluded)", selected.ID)
	}
}

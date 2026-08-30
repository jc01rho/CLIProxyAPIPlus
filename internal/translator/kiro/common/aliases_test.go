package common

import "testing"

func TestSupportsKiroThinkingAllowlists(t *testing.T) {
	if SupportsKiroAdaptiveThinking("claude-sonnet-5") {
		t.Fatal("claude-sonnet-5 must not receive an unverified adaptive envelope")
	}
	if !SupportsKiroAdaptiveThinking("kiro-claude-sonnet-4-6-thinking") {
		t.Fatal("prefixed sonnet-4.6 thinking alias must still match")
	}
	if SupportsKiroAdaptiveThinking("claude-sonnet-4.5") {
		t.Fatal("claude-sonnet-4.5 must not receive the adaptive envelope")
	}
	if !SupportsKiroAdaptiveThinking("claude-opus-4.6") || !SupportsKiroAdaptiveThinking("kiro-claude-sonnet-4-6") {
		t.Fatal("kiro-lb adaptive set must include opus-4.6 and sonnet-4.6")
	}
	if SupportsKiroNativeReasoning("gpt-5.6-sol") || SupportsKiroNativeReasoning("kiro-gpt-5-6-luna") {
		t.Fatal("Kiro CLI does not send native GPT reasoning fields")
	}
	if SupportsKiroNativeReasoning("claude-sonnet-5") {
		t.Fatal("claude-sonnet-5 must not use native reasoning")
	}
}

func TestIsObsoleteKiroRequestAlias(t *testing.T) {
	cases := map[string]bool{
		"auto-kiro":                      false,
		"kiro-auto":                      false,
		"kiro-claude-sonnet-4-5-agentic": false,
		"claude-sonnet-5-thinking":       false,
		"kiro-claude-sonnet-5-thinking":  false,
		"claude-sonnet-4.5-thinking":     false,
		"claude-sonnet-5":                false,
		"claude-sonnet-5[1m]":            false,
	}
	for id, want := range cases {
		if got := IsObsoleteKiroRequestAlias(id); got != want {
			t.Errorf("IsObsoleteKiroRequestAlias(%q)=%v, want %v", id, got, want)
		}
	}
}

func TestHasUnsupportedKiroContextSuffix(t *testing.T) {
	if !HasUnsupportedKiroContextSuffix("claude-sonnet-5-thinking[1m]") {
		t.Fatal("expected [1m] detect")
	}
	if HasUnsupportedKiroContextSuffix("claude-sonnet-5") {
		t.Fatal("plain id must pass")
	}
}

func TestEffortFromThinkingBudgetWithMax(t *testing.T) {
	if got := EffortFromThinkingBudgetWithMax(512, 8000); got != "" {
		t.Fatalf("budget below 1024 = %q, want empty", got)
	}
	cases := []struct {
		budget int64
		want   string
	}{
		{8000, "max"},
		{6000, "xhigh"},
		{4000, "high"},
		{2000, "medium"},
		{1100, "low"},
	}
	for _, tc := range cases {
		if got := EffortFromThinkingBudgetWithMax(tc.budget, 8000); got != tc.want {
			t.Errorf("budget %d = %q, want %q", tc.budget, got, tc.want)
		}
	}
}

func TestPlanKiroThinkingGatesEnvelope(t *testing.T) {
	adaptive := PlanKiroThinking("claude-sonnet-4.6", true, "high", 8000)
	if !adaptive.AdaptiveThinking || adaptive.Fields == nil {
		t.Fatalf("sonnet-4.6 plan = %+v", adaptive)
	}
	if _, ok := adaptive.Fields["output_config"]; !ok {
		t.Fatal("sonnet-4.6 missing output_config")
	}
	if _, ok := adaptive.Fields["max_tokens"]; ok {
		t.Fatal("adaptive envelope must not include unknown max_tokens")
	}
	if _, ok := adaptive.Fields["reasoning"]; ok {
		t.Fatal("sonnet-4.6 must not get native reasoning envelope")
	}
	if adaptive.InjectPrompt {
		t.Fatal("adaptive models must not inject prompt tags")
	}

	none := PlanKiroThinking("claude-sonnet-4.6", false, "", 0)
	if none.Fields != nil {
		t.Fatalf("no-effort request must omit additional fields: %+v", none.Fields)
	}

	unverified := PlanKiroThinking("glm-5", true, "max", 0)
	if unverified.Fields != nil {
		t.Fatalf("unverified model must not receive the field: %+v", unverified.Fields)
	}

	native := PlanKiroThinking("gpt-5.6-terra", true, "max", 0)
	if native.NativeReasoning || native.InjectPrompt || native.Fields != nil {
		t.Fatalf("gpt plan must omit unsupported request fields: %+v", native)
	}

	legacy := PlanKiroThinking("claude-sonnet-4.5", true, "", 0)
	if legacy.Fields != nil {
		t.Fatalf("legacy sonnet-4.5 must not attach additional fields: %+v", legacy.Fields)
	}
	if legacy.InjectPrompt || legacy.ThinkingLength != 0 {
		t.Fatalf("legacy model must omit prompt emulation: %+v", legacy)
	}
}

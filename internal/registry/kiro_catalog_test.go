package registry

import "testing"

func TestOverlayStaticMetadataKeepsLiveIDsOnly(t *testing.T) {
	dynamic := []*ModelInfo{
		{ID: "kiro-claude-sonnet-5", DisplayName: "Kiro Claude Sonnet 5", ContextLength: 1000000},
		{ID: "kiro-minimax-m2-5", DisplayName: "Kiro MiniMax M2.5", ContextLength: 200000},
	}
	static := GetKiroModels()

	merged := OverlayStaticMetadata(dynamic, static)
	if len(merged) != 2 {
		t.Fatalf("overlay len = %d, want 2 (live IDs only)", len(merged))
	}

	seen := map[string]bool{}
	for _, m := range merged {
		seen[m.ID] = true
	}
	if !seen["kiro-claude-sonnet-5"] || !seen["kiro-minimax-m2-5"] {
		t.Fatalf("missing live IDs: %+v", seen)
	}
	for _, fabricated := range []string{"kiro-auto", "kiro-claude-opus-4-6", "kiro-gpt-4o"} {
		if seen[fabricated] {
			t.Fatalf("fabricated static ID %s leaked into live overlay", fabricated)
		}
	}
}

func TestGetKiroModelsIncludesOmniRouteVerifiedIDs(t *testing.T) {
	want := []string{
		"kiro-claude-sonnet-5",
		"kiro-claude-sonnet-5-thinking",
		"kiro-minimax-m2-5",
		"kiro-glm-5",
		"kiro-gpt-5-6-sol",
		"kiro-gpt-5-6-terra",
		"kiro-gpt-5-6-luna",
		"kiro-claude-sonnet-4-5",
		"kiro-claude-haiku-4-5",
		"kiro-deepseek-3-2",
		"kiro-minimax-m2-1",
		"kiro-qwen3-coder-next",
		"kiro-claude-opus-4-7",
		"kiro-claude-opus-4-8",
		"kiro-claude-opus-5",
	}
	got := map[string]*ModelInfo{}
	for _, m := range GetKiroModels() {
		got[m.ID] = m
	}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("static catalog missing verified id %s", id)
		}
	}

	sonnet5 := got["kiro-claude-sonnet-5"]
	if sonnet5 == nil {
		t.Fatal("kiro-claude-sonnet-5 missing")
	}
	if sonnet5.ContextLength != 666667 || sonnet5.MaxCompletionTokens != 64000 {
		t.Errorf("sonnet-5 windows = %d/%d, want 666667/64k", sonnet5.ContextLength, sonnet5.MaxCompletionTokens)
	}
	opus47 := got["kiro-claude-opus-4-7"]
	if opus47 == nil {
		t.Fatal("kiro-claude-opus-4-7 missing")
	}
	if opus47.ContextLength != 666667 || opus47.MaxCompletionTokens != 128000 {
		t.Errorf("opus-4.7 windows = %d/%d, want 666667/128k", opus47.ContextLength, opus47.MaxCompletionTokens)
	}
	sol := got["kiro-gpt-5-6-sol"]
	if sol == nil {
		t.Fatal("kiro-gpt-5-6-sol missing")
	}
	if sol.ContextLength != 272000 || sol.MaxCompletionTokens != 128000 {
		t.Errorf("gpt-5.6-sol windows = %d/%d, want 272k/128k", sol.ContextLength, sol.MaxCompletionTokens)
	}
}

func TestConvertKiroAPIModelsAcceptsAvailableModelsShape(t *testing.T) {
	converted := ConvertKiroAPIModels([]*KiroAPIModel{
		{ModelID: "claude-sonnet-5", ModelName: "Claude Sonnet 5", RateMultiplier: 1.3, MaxInputTokens: 1000000},
		{ModelID: "gpt-5.6-sol", ModelName: "GPT-5.6 Sol", RateMultiplier: 1, MaxInputTokens: 272000},
		{ModelID: "claude-sonnet-5", ModelName: "dup"},
		{ModelID: ""},
	})
	if len(converted) != 2 {
		t.Fatalf("converted = %d, want 2", len(converted))
	}
	if converted[0].ID != "kiro-claude-sonnet-5" {
		t.Errorf("id = %s, want kiro-claude-sonnet-5", converted[0].ID)
	}
	if converted[0].ContextLength != 1000000 {
		t.Errorf("ctx = %d, want 1000000", converted[0].ContextLength)
	}
	if converted[0].DisplayName != "Kiro Claude Sonnet 5 (1.3x credit)" {
		t.Errorf("display = %q", converted[0].DisplayName)
	}
	if converted[1].ID != "kiro-gpt-5-6-sol" {
		t.Errorf("id = %s, want kiro-gpt-5-6-sol", converted[1].ID)
	}
}

package registry

import "testing"

func TestRegisterClientPrefersRealModelInfoForDuplicateModelID(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{
		{ID: "gpt-5.4", ExecutionTarget: "gpt-5.2", DisplayName: "alias exposed"},
		{ID: "gpt-5.4", DisplayName: "real model"},
		{ID: "gpt-5.2", DisplayName: "upstream"},
	})

	models := r.GetModelsForClient("client-1")
	if len(models) != 2 {
		t.Fatalf("expected 2 deduped models, got %d", len(models))
	}
	if models[0] == nil || models[0].ID != "gpt-5.4" {
		t.Fatalf("expected first model to be gpt-5.4, got %+v", models)
	}
	if models[0].ExecutionTarget != "" {
		t.Fatalf("expected duplicate model id to retain real metadata, got execution target %q", models[0].ExecutionTarget)
	}
	if models[0].DisplayName != "real model" {
		t.Fatalf("expected duplicate model id to retain real model display name, got %q", models[0].DisplayName)
	}
}

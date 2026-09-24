package registry

import "testing"

// A model id served by several credentials must keep reasoning metadata when a
// later registration carries none. Gateway catalogs list ids without thinking
// data; letting them overwrite a statically described model made Claude Opus 5
// and Sonnet 5 advertise no thinking support on /v1/models.
func TestRegisterClientKeepsThinkingFromAnotherClient(t *testing.T) {
	r := newTestModelRegistry()
	thinking := &ThinkingSupport{ZeroAllowed: true, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}}
	r.RegisterClient("config-key", "claude", []*ModelInfo{{ID: "opus", DisplayName: "Opus", Thinking: thinking}})
	r.RegisterClient("gateway-oauth", "claude", []*ModelInfo{{ID: "opus", DisplayName: "Opus"}})

	info := r.GetModelInfo("opus", "claude")
	if info == nil || info.Thinking == nil {
		t.Fatalf("expected thinking kept from the other client, got %+v", info)
	}
	if len(info.Thinking.Levels) != 3 {
		t.Fatalf("levels = %v", info.Thinking.Levels)
	}
	if !modelMapHasThinking(r.GetAvailableModels("claude"), "opus") {
		t.Fatal("expected /v1/models claude listing to advertise thinking for opus")
	}
}

func TestRegisterClientDropsThinkingWhenNoClientHasIt(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("a", "claude", []*ModelInfo{{ID: "plain"}})
	r.RegisterClient("b", "claude", []*ModelInfo{{ID: "plain"}})
	if info := r.GetModelInfo("plain", "claude"); info == nil || info.Thinking != nil {
		t.Fatalf("expected no thinking, got %+v", info)
	}
}

func modelMapHasThinking(models []map[string]any, id string) bool {
	for _, model := range models {
		if model["id"] == id {
			value, _ := model["thinking"].(bool)
			return value
		}
	}
	return false
}

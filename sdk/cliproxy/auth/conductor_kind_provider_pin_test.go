package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Codex realtime selects with SelectAuthByKind("codex", "", OAuth). Under
// weight-robin the cross-provider cycle used to hand it an Antigravity token,
// which api.openai.com rejected as "Incorrect API key provided: ya29...".
func TestSelectAuthByKindStaysOnProviderUnderWeightedRobin(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &WeightedRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	manager.RegisterExecutor(schedulerTestExecutor{provider: "antigravity"})
	for _, candidate := range []*Auth{
		{ID: "pin-antigravity-a", Provider: "antigravity", Attributes: map[string]string{"priority": "100"}, Metadata: map[string]any{"access_token": "ya29.test"}},
		{ID: "pin-antigravity-b", Provider: "antigravity", Attributes: map[string]string{"priority": "100"}, Metadata: map[string]any{"access_token": "ya29.test2"}},
		{ID: "pin-codex", Provider: "codex", Attributes: map[string]string{"priority": "0"}, Metadata: map[string]any{"access_token": "codex-token"}},
	} {
		if _, errRegister := manager.Register(ctx, candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}
	for i := 0; i < 6; i++ {
		selected, errSelect := manager.SelectAuthByKind(ctx, "codex", "", AuthKindOAuth, cliproxyexecutor.Options{})
		if errSelect != nil || selected == nil || selected.ID != "pin-codex" {
			t.Fatalf("selection %d = %#v, %v, want pin-codex", i, selected, errSelect)
		}
	}
}

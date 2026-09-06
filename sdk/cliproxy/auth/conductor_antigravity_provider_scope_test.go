package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestAntigravityPrimaryExclusionStaysWithinProvider(t *testing.T) {
	primary := &Auth{
		ID: "ag-primary", Provider: "antigravity", Status: StatusActive,
		PrimaryInfo: &PrimaryInfo{IsPrimary: true, Order: 1},
	}
	standby := &Auth{
		ID: "ag-standby", Provider: "antigravity", Status: StatusActive,
		PrimaryInfo: &PrimaryInfo{IsPrimary: false, Order: 2},
	}
	for _, provider := range []string{"zcode", "claude", "codex", "openai-compatible-pool"} {
		t.Run(provider, func(t *testing.T) {
			candidate := &Auth{ID: "other-provider", Provider: provider, Status: StatusActive}
			auths := []*Auth{primary, standby, candidate}
			if soleAntigravityPrimaryExcluded("antigravity", auths, candidate) {
				t.Fatalf("Antigravity primary excluded healthy %s credential", provider)
			}
			if soleAntigravityPrimaryExcluded("antigravity", auths, primary) {
				t.Fatal("primary must remain eligible")
			}
			if !soleAntigravityPrimaryExcluded("antigravity", auths, standby) {
				t.Fatal("Antigravity standby must remain excluded")
			}
		})
	}
}

func TestManagerAliasWithAntigravityPrimaryKeepsOwnProvider(t *testing.T) {
	for _, shape := range []string{"oauth-glm", "claude-key-opus"} {
		for _, path := range []string{"execute", "stream"} {
			t.Run(shape+"/"+path, func(t *testing.T) {
				f := newAliasAvailabilityFixture(t, &WeightedRobinSelector{}, shape, false)
				primary := &Auth{
					ID: "ag-primary-" + t.Name(), Provider: "antigravity", Status: StatusActive,
					PrimaryInfo: &PrimaryInfo{IsPrimary: true, Order: 1},
				}
				fallbackExecutor := &aliasModelPoolExecutor{provider: "antigravity"}
				f.manager.RegisterExecutor(fallbackExecutor)
				if _, err := f.manager.Register(context.Background(), primary); err != nil {
					t.Fatal(err)
				}
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(primary.ID, primary.Provider, []*registry.ModelInfo{{ID: "higher-coding"}})
				t.Cleanup(func() { reg.UnregisterClient(primary.ID) })
				if f.route == "opus" {
					f.manager.SetFallbackModels(map[string]string{"opus": "higher-coding"})
				} else {
					f.manager.SetFallbackChain([]string{"higher-coding"}, 5)
				}

				got, err := f.run(t, path, "unwrapped")

				want := f.auth.ID + ":" + f.targets[0]
				if err != nil || got != want {
					t.Fatalf("healthy %s: payload=%q error=%v, want own provider %q", f.route, got, err, want)
				}
				if count := len(fallbackExecutor.ExecuteCalls()) + len(fallbackExecutor.StreamCalls()); count != 0 {
					t.Fatalf("healthy %s fell back to Antigravity %d times", f.route, count)
				}
			})
		}
	}
}

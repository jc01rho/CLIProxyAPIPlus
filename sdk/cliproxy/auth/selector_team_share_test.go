package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestTeamShareSelectorPrefersAccountWithMostRemainingUsage(t *testing.T) {
	remaining := map[string]float64{"pool-a": 0.20, "pool-b": 0.70, "pool-c": 0.69}
	selector := NewTeamShareSelector(&RoundRobinSelector{}, func(id string) (float64, bool) {
		v, ok := remaining[id]
		return v, ok
	})
	auths := []*Auth{
		{ID: "pool-a", Provider: "claude"},
		{ID: "pool-b", Provider: "claude"},
		{ID: "pool-c", Provider: "claude"},
		{ID: "outside", Provider: "claude"},
	}
	got := map[string]int{}
	for i := 0; i < 12; i++ {
		picked, err := selector.Pick(context.Background(), "claude", "claude-sonnet-5-5", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatal(err)
		}
		got[picked.ID]++
	}
	if got["pool-a"] != 0 {
		t.Fatalf("the nearly exhausted account must not be chosen: %v", got)
	}
	if got["pool-b"] == 0 || got["pool-c"] == 0 || got["outside"] == 0 {
		t.Fatalf("near-equal and non-pool accounts must still share the load: %v", got)
	}
}

func TestTeamShareSelectorLeavesNonPoolCandidatesToTheWrappedSelector(t *testing.T) {
	selector := NewTeamShareSelector(&RoundRobinSelector{}, func(string) (float64, bool) { return 0, false })
	auths := []*Auth{{ID: "x", Provider: "claude"}, {ID: "y", Provider: "claude"}}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		picked, err := selector.Pick(context.Background(), "claude", "m", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatal(err)
		}
		seen[picked.ID] = true
	}
	if !seen["x"] || !seen["y"] {
		t.Fatalf("round robin must be preserved outside pools: %v", seen)
	}
}

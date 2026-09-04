package auth

import (
	"context"
	"testing"
	"time"
)

// Regression tests: the antigravity primary/standby invariant must keep exactly one
// managed credential serving traffic without breaking legacy fallback credentials.

func TestAntigravityEnabledStandbyDemotedAtRegister(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	primary := &Auth{
		ID:          "ag-primary",
		Provider:    "antigravity",
		Status:      StatusActive,
		PrimaryInfo: &PrimaryInfo{IsPrimary: true, Order: 1},
	}
	if _, errRegister := manager.Register(ctx, primary); errRegister != nil {
		t.Fatalf("Register(primary) error = %v", errRegister)
	}

	// A managed standby that leaked into the enabled state (dirty file/hand-merge).
	leaked := &Auth{
		ID:          "ag-leaked-standby",
		Provider:    "antigravity",
		Status:      StatusActive,
		PrimaryInfo: &PrimaryInfo{IsPrimary: false, Order: 2},
	}
	if _, errRegister := manager.Register(ctx, leaked); errRegister != nil {
		t.Fatalf("Register(leaked) error = %v", errRegister)
	}

	stored, ok := manager.GetByID("ag-leaked-standby")
	if !ok || stored == nil {
		t.Fatal("leaked standby missing from manager")
	}
	if !stored.Disabled || stored.Status != StatusDisabled {
		t.Fatalf("leaked standby not demoted: disabled=%v status=%v", stored.Disabled, stored.Status)
	}
}

func TestAntigravityDuplicatePrimaryClaimsRegisterSoleWinner(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	first := &Auth{
		ID:          "ag-claim-a",
		Provider:    "antigravity",
		Status:      StatusActive,
		PrimaryInfo: &PrimaryInfo{IsPrimary: true, Order: 1},
	}
	if _, errRegister := manager.Register(ctx, first); errRegister != nil {
		t.Fatalf("Register(first) error = %v", errRegister)
	}
	second := &Auth{
		ID:          "ag-claim-b",
		Provider:    "antigravity",
		Status:      StatusActive,
		PrimaryInfo: &PrimaryInfo{IsPrimary: true, Order: 2},
	}
	if _, errRegister := manager.Register(ctx, second); errRegister != nil {
		t.Fatalf("Register(second) error = %v", errRegister)
	}

	primaries := 0
	for _, auth := range manager.List() {
		if auth.Provider == "antigravity" && auth.PrimaryInfo != nil && auth.PrimaryInfo.IsPrimary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("primary antigravity auths = %d, want 1", primaries)
	}
}

func TestAntigravityEnablePromotesAndDemotesPreviousPrimary(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	first := &Auth{
		ID:          "ag-ena-a",
		Provider:    "antigravity",
		Status:      StatusActive,
		PrimaryInfo: &PrimaryInfo{IsPrimary: true, Order: 1},
	}
	if _, errRegister := manager.Register(ctx, first); errRegister != nil {
		t.Fatalf("Register(first) error = %v", errRegister)
	}
	standby := &Auth{
		ID:          "ag-ena-b",
		Provider:    "antigravity",
		Status:      StatusDisabled,
		Disabled:    true,
		PrimaryInfo: &PrimaryInfo{IsPrimary: false, Order: 2},
	}
	if _, errRegister := manager.Register(ctx, standby); errRegister != nil {
		t.Fatalf("Register(standby) error = %v", errRegister)
	}

	// Management enable of the standby is a promotion request: it becomes the sole
	// primary and the previous primary stands down.
	promoted := &Auth{
		ID:          "ag-ena-b",
		Provider:    "antigravity",
		Status:      StatusActive,
		PrimaryInfo: &PrimaryInfo{IsPrimary: false, Order: 2},
	}
	if _, errUpdate := manager.Update(ctx, promoted); errUpdate != nil {
		t.Fatalf("Update(promoted) error = %v", errUpdate)
	}

	newPrimary, ok := manager.GetByID("ag-ena-b")
	if !ok || newPrimary == nil || newPrimary.Disabled || newPrimary.PrimaryInfo == nil || !newPrimary.PrimaryInfo.IsPrimary {
		t.Fatalf("promoted standby not primary: %+v", newPrimary)
	}
	oldPrimary, ok := manager.GetByID("ag-ena-a")
	if !ok || oldPrimary == nil || !oldPrimary.Disabled || oldPrimary.PrimaryInfo == nil || oldPrimary.PrimaryInfo.IsPrimary {
		t.Fatalf("previous primary not demoted: %+v", oldPrimary)
	}
}

func TestAntigravityLegacyFallbackCredentialsStayEnabled(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	legacyA := &Auth{ID: "aa-legacy", Provider: "antigravity", Status: StatusActive}
	legacyB := &Auth{ID: "bb-legacy", Provider: "antigravity", Status: StatusActive}
	for _, auth := range []*Auth{legacyA, legacyB} {
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}

	now := time.Now()
	available, errSel := manager.availableAuthsForRouteModel(manager.List(), "antigravity", "gemini-3-pro", now)
	if errSel != nil {
		t.Fatalf("availableAuthsForRouteModel() error = %v", errSel)
	}
	if len(available) != 2 {
		t.Fatalf("selection candidates = %d, want 2 (legacy fallback pair untouched)", len(available))
	}
}

func TestAntigravitySelectionExcludesDuplicatePrimaryClaims(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	both := []*Auth{
		{ID: "ag-dup-a", Provider: "antigravity", Status: StatusActive, PrimaryInfo: &PrimaryInfo{IsPrimary: true, Order: 1}},
		{ID: "ag-dup-b", Provider: "antigravity", Status: StatusActive, PrimaryInfo: &PrimaryInfo{IsPrimary: true, Order: 2}},
	}
	available, errSel := manager.availableAuthsForRouteModel(both, "antigravity", "gemini-3-pro", now)
	if errSel != nil {
		t.Fatalf("availableAuthsForRouteModel() error = %v", errSel)
	}
	if len(available) != 1 {
		t.Fatalf("selection candidates = %d, want 1", len(available))
	}
	if available[0].ID != "ag-dup-a" {
		t.Fatalf("selection winner = %s, want ag-dup-a (lowest order wins)", available[0].ID)
	}
}

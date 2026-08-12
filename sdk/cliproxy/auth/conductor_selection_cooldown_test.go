package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestBuiltInSelectorCooldownErrorPreservesRouteModel(t *testing.T) {
	t.Parallel()

	const routeModel = "client-opus(high)"
	next := time.Now().Add(time.Hour)
	auth := &Auth{
		ID:             "cooling-auth",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota: QuotaState{
			Exceeded:      true,
			NextRecoverAt: next,
		},
		ModelStates: map[string]*ModelState{
			"other-model": {Status: StatusActive},
		},
	}

	selectors := map[string]Selector{
		"round-robin":          &RoundRobinSelector{},
		"weighted-round-robin": &WeightedRoundRobinSelector{},
		"fill-first":           &FillFirstSelector{},
	}
	for name, selector := range selectors {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, errPick := selector.Pick(
				context.Background(),
				"mixed",
				selectionArgForSelector(selector, routeModel),
				cliproxyexecutor.Options{},
				[]*Auth{auth},
			)
			if errPick == nil {
				t.Fatal("Pick() error = nil, want model cooldown")
			}

			errPick = restoreModelCooldownErrorModel(errPick, routeModel)
			var cooldownErr *modelCooldownError
			if !errors.As(errPick, &cooldownErr) {
				t.Fatalf("Pick() error = %T, want *modelCooldownError", errPick)
			}
			if cooldownErr.model != routeModel {
				t.Fatalf("cooldown model = %q, want %q", cooldownErr.model, routeModel)
			}
		})
	}
}

func TestReconcileRegistryModelStatesPreservesActiveQuotaCooldown(t *testing.T) {
	const (
		authID   = "reconcile-active-quota-auth"
		provider = "openai-compatible-synthetic"
		model    = "higher-coding"
	)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: provider,
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit",
			Message:    "subscription rate limit exceeded",
			HTTPStatus: 429,
		},
	})

	before, ok := manager.GetByID(authID)
	if !ok || before == nil || before.ModelStates[model] == nil {
		t.Fatal("expected active model quota state before reconciliation")
	}
	beforeState := before.ModelStates[model]
	if beforeState.Quota.BackoffLevel != 1 || !beforeState.Quota.Exceeded || beforeState.NextRetryAfter.IsZero() {
		t.Fatalf("unexpected quota state before reconciliation: %+v", beforeState)
	}

	manager.ReconcileRegistryModelStates(context.Background(), authID)

	after, ok := manager.GetByID(authID)
	if !ok || after == nil || after.ModelStates[model] == nil {
		t.Fatal("expected model quota state after reconciliation")
	}
	afterState := after.ModelStates[model]
	if afterState.Quota.BackoffLevel != beforeState.Quota.BackoffLevel {
		t.Fatalf("BackoffLevel = %d, want %d", afterState.Quota.BackoffLevel, beforeState.Quota.BackoffLevel)
	}
	if !afterState.Quota.Exceeded {
		t.Fatal("quota cooldown was cleared during model reconciliation")
	}
	if !afterState.NextRetryAfter.Equal(beforeState.NextRetryAfter) {
		t.Fatalf("NextRetryAfter = %v, want %v", afterState.NextRetryAfter, beforeState.NextRetryAfter)
	}
	if blocked, _, _ := isAuthBlockedForModel(after, model, time.Now()); !blocked {
		t.Fatal("auth became selectable while its quota cooldown was active")
	}
}

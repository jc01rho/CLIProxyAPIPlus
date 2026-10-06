package registry

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewUnknownModelRefreshesModelsDevSnapshotOnce(t *testing.T) {
	previous := modelsDevLimits.Load()
	t.Cleanup(func() {
		modelsDevLimits.Store(previous)
		notifyModelsDevRefreshIdle = nil
	})
	modelsDevLimits.Store(&modelsDevLimitCatalog{
		exact: map[string]ModelsDevLimit{"known-model": {Context: 1000, Output: 100}},
	})

	reg := newTestModelRegistry()
	var calls atomic.Int32
	idle := make(chan struct{}, 1)
	notifyModelsDevRefreshIdle = func() { idle <- struct{}{} }
	reg.SetRefreshUnknownModels(func(context.Context) error {
		calls.Add(1)
		return nil
	})

	reg.RegisterClient("client-a", "mistral", []*ModelInfo{{ID: "known-model"}, {ID: "mistral-large-4"}})
	waitModelsDevRefreshIdle(t, idle)
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}

	reg.RegisterClient("client-a", "mistral", []*ModelInfo{{ID: "known-model"}, {ID: "mistral-large-4"}})
	if got := calls.Load(); got != 1 {
		t.Fatalf("re-registration refresh calls = %d, want 1", got)
	}
}

func TestModelsDevRefreshRerunsWhenAnotherUnknownModelArrives(t *testing.T) {
	previous := modelsDevLimits.Load()
	t.Cleanup(func() {
		modelsDevLimits.Store(previous)
		notifyModelsDevRefreshIdle = nil
	})
	modelsDevLimits.Store(&modelsDevLimitCatalog{exact: map[string]ModelsDevLimit{}})

	reg := newTestModelRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	idle := make(chan struct{}, 1)
	notifyModelsDevRefreshIdle = func() { idle <- struct{}{} }
	reg.SetRefreshUnknownModels(func(context.Context) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	})

	reg.RegisterClient("client-a", "mistral", []*ModelInfo{{ID: "mistral-large-4"}})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first refresh did not start")
	}
	reg.RegisterClient("client-b", "mistral", []*ModelInfo{{ID: "higher-coding"}})
	close(release)
	waitModelsDevRefreshIdle(t, idle)
	if got := calls.Load(); got != 2 {
		t.Fatalf("refresh calls = %d, want 2", got)
	}
}

func waitModelsDevRefreshIdle(t *testing.T, idle <-chan struct{}) {
	t.Helper()
	select {
	case <-idle:
	case <-time.After(2 * time.Second):
		t.Fatal("models.dev refresh did not finish")
	}
}

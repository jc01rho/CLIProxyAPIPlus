package registry

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestClineModelsUpdaterNotifiesAndStopsOnCancellation(t *testing.T) {
	refreshCallbackMu.Lock()
	previous, pending := refreshCallback, pendingRefreshChanges
	notifications := make(chan []string, 1)
	refreshCallback = func(providers []string) { notifications <- providers }
	refreshCallbackMu.Unlock()
	t.Cleanup(func() {
		refreshCallbackMu.Lock()
		refreshCallback, pendingRefreshChanges = previous, pending
		refreshCallbackMu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runClineModelsUpdater(ctx)
		close(done)
	}()
	select {
	case providers := <-notifications:
		if !reflect.DeepEqual(providers, []string{"cline"}) {
			t.Fatalf("providers = %v", providers)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing initial Cline refresh signal")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("updater did not stop on cancellation")
	}
}

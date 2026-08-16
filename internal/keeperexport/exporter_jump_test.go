package keeperexport

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestExporterOneShotJumpFromStaleHeader verifies option 3: when the keeper
// reports a stale_revision and includes X-Keeper-Export-Current-Revision, the
// exporter jumps straight to currentRevision+1 in a single supersede instead of
// walking revision-by-revision up to the recovery budget.
func TestExporterOneShotJumpFromStaleHeader(t *testing.T) {
	fake := newFakeKeeper()
	fake.staleRevisionFrom.Store(1)
	fake.keeperCurrentRevision.Store(1000)
	server := httptest.NewTLSServer(fake)
	defer server.Close()
	cfg := testExporterConfig(t, server)
	t.Setenv(cfg.Keeper.TokenEnv, "token-one")
	var runtime Runtime
	runtime.SetSnapshotSource(func() SnapshotInput { return SnapshotInput{} })
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	awaitSignal(t, fake.identitySeen)
	awaitAuthFilesRevision(t, fake, 1001)
	fake.mu.Lock()
	revisions := append([]int64(nil), fake.metadataRevisions[string(CategoryAuthFiles)]...)
	fake.mu.Unlock()
	if len(revisions) != 2 || revisions[0] != 1 || revisions[1] != 1001 {
		t.Fatalf("auth_files revisions=%v, want [1 1001] (one-shot jump)", revisions)
	}
}

// TestExporterFloorSurvivesOutboxReset verifies option 2: the durable floor
// recorded from the keeper current revision makes a fresh outbox resume above it
// instead of from revision 1 after a local reset.
func TestExporterFloorSurvivesOutboxReset(t *testing.T) {
	fake := newFakeKeeper()
	fake.keeperCurrentRevision.Store(50)
	server := httptest.NewTLSServer(fake)
	defer server.Close()
	cfg := testExporterConfig(t, server)
	t.Setenv(cfg.Keeper.TokenEnv, "token-one")
	var runtime Runtime
	runtime.SetSnapshotSource(func() SnapshotInput { return SnapshotInput{} })
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, fake.identitySeen)
	awaitAuthFilesRevision(t, fake, 1)
	awaitAllMetadataCategories(t, fake)

	runtime.mu.RLock()
	outbox := runtime.outbox
	runtime.mu.RUnlock()
	if err := outbox.DeleteMetadataUntilRevisionForTest(CategoryAuthFiles); err != nil {
		t.Fatal(err)
	}
	if err := outbox.SetMetadataRevisionFloor(context.Background(), CategoryAuthFiles, 50); err != nil {
		t.Fatal(err)
	}
	runtime.Close()
	drainSignals(fake.metadataSeen)
	drainSignals(fake.identitySeen)

	var runtime2 Runtime
	runtime2.SetSnapshotSource(func() SnapshotInput { return SnapshotInput{} })
	cfg2 := cfg
	cfg2.Outbox.Path = cfg.Outbox.Path
	if err := runtime2.Apply(context.Background(), cfg2); err != nil {
		t.Fatal(err)
	}
	defer runtime2.Close()
	awaitSignal(t, fake.identitySeen)
	awaitAuthFilesRevision(t, fake, 51)
	fake.mu.Lock()
	revisions := append([]int64(nil), fake.metadataRevisions[string(CategoryAuthFiles)]...)
	fake.mu.Unlock()
	if len(revisions) < 2 || revisions[len(revisions)-1] != 51 {
		t.Fatalf("auth_files revisions=%v, want last delivery at 51 (floor=50)", revisions)
	}
}

func awaitAuthFilesRevision(t *testing.T, fake *fakeKeeper, want int64) {
	t.Helper()
	if authFilesHasRevision(fake, want) {
		return
	}
	for range 8 {
		awaitSignal(t, fake.metadataSeen)
		if authFilesHasRevision(fake, want) {
			return
		}
	}
	t.Fatalf("timed out waiting for auth_files revision %d, got %v", want, authFilesRevisions(fake))
}

func awaitAllMetadataCategories(t *testing.T, fake *fakeKeeper) {
	t.Helper()
	if metadataCategoryCount(fake) >= 3 {
		return
	}
	for range 8 {
		awaitSignal(t, fake.metadataSeen)
		if metadataCategoryCount(fake) >= 3 {
			return
		}
	}
	t.Fatalf("timed out waiting for all metadata categories, got %d", metadataCategoryCount(fake))
}

func authFilesHasRevision(fake *fakeKeeper, want int64) bool {
	for _, rev := range authFilesRevisions(fake) {
		if rev == want {
			return true
		}
	}
	return false
}

func authFilesRevisions(fake *fakeKeeper) []int64 {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]int64(nil), fake.metadataRevisions[string(CategoryAuthFiles)]...)
}

func metadataCategoryCount(fake *fakeKeeper) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.metadataRevisions)
}

func drainSignals(signal <-chan struct{}) {
	for {
		select {
		case <-signal:
		default:
			return
		}
	}
}

package keeperexport

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestRuntimeHotReloadEnableDisableAndAppendNeverHangs(t *testing.T) {
	var runtime Runtime
	path := filepath.Join(t.TempDir(), "outbox.db")
	cfg := config.DefaultUsageExportConfig(filepath.Dir(path))
	cfg.Enabled = true
	cfg.Mode = config.UsageExportModePush
	cfg.Outbox.Path = path
	cfg.Outbox.MaxBytes = 16 << 20

	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("Apply(enabled) error = %v", err)
	}
	const writers = 32
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := runtime.Append(ctx, []byte(`{"request_id":"duplicate"}`)); err != nil {
				t.Errorf("Append() error = %v", err)
			}
		}()
	}
	wg.Wait()

	disabled := config.DefaultUsageExportConfig(filepath.Dir(path))
	if err := runtime.Apply(context.Background(), disabled); err != nil {
		t.Fatalf("Apply(disabled) error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Append(ctx, []byte("ignored")); err != nil {
		t.Fatalf("Append(disabled) error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}

	outbox, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	status, err := outbox.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BacklogEvents != writers {
		t.Fatalf("backlog events = %d, want %d", status.BacklogEvents, writers)
	}
}

func TestRuntimeDisableTrulyOverlapsBlockedAppend(t *testing.T) {
	var runtime Runtime
	dir := t.TempDir()
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled = true
	cfg.Mode = config.UsageExportModePush
	cfg.Outbox.MaxBytes = 16 << 20
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	runtime.mu.RLock()
	runtime.outbox.testHooks.beforeAppendCommit = func(context.Context) error {
		close(entered)
		<-release
		return nil
	}
	runtime.mu.RUnlock()
	appendDone := make(chan error, 1)
	go func() { appendDone <- runtime.Append(context.Background(), []byte("overlap")) }()
	<-entered
	disabled := config.DefaultUsageExportConfig(dir)
	applyDone := make(chan error, 1)
	go func() { applyDone <- runtime.Apply(context.Background(), disabled) }()
	select {
	case err := <-applyDone:
		t.Fatalf("disable completed before blocked append released: %v", err)
	default:
	}
	close(release)
	if err := <-appendDone; err != nil {
		t.Fatal(err)
	}
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeFailedReloadKeepsPreviousOutbox(t *testing.T) {
	var runtime Runtime
	dir := t.TempDir()
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled = true
	cfg.Mode = config.UsageExportModePush
	cfg.Outbox.MaxBytes = 16 << 20
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.Append(context.Background(), []byte("before")); err != nil {
		t.Fatal(err)
	}

	badParent := filepath.Join(dir, "not-dir")
	if err := os.WriteFile(badParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := cfg
	bad.Outbox.Path = filepath.Join(badParent, "outbox.db")
	if err := runtime.Apply(context.Background(), bad); err == nil {
		t.Fatal("Apply(bad path) error = nil")
	}
	if err := runtime.Append(context.Background(), []byte("after")); err != nil {
		t.Fatalf("previous runtime stopped after failed reload: %v", err)
	}
	status, err := runtime.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BacklogEvents != 2 {
		t.Fatalf("backlog events = %d, want 2", status.BacklogEvents)
	}
}

package keeperexport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutboxLiteralQuestionAndFragmentPathAndLiveFilesOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "literal?#outbox.db")
	outbox, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	payload := []byte(`{"exact":"payload"}`)
	if _, err = outbox.Append(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("literal target was not created: %v", err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, statErr := os.Stat(candidate)
		if statErr != nil {
			t.Fatalf("expected live SQLite file %q: %v", candidate, statErr)
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("live SQLite file %q mode = %#o, want 0600 or stricter", candidate, got)
		}
	}
	items, err := outbox.List(context.Background(), 1, 1024)
	if err != nil || len(items) != 1 || !bytes.Equal(items[0].Payload, payload) {
		t.Fatalf("literal path payload = %#v, %v", items, err)
	}
}

func TestOutboxMaxSafeSequenceAppendsOnceAndSurvivesRestartExhausted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	outbox, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = outbox.db.Exec(`UPDATE outbox_meta SET value=? WHERE key='next_sequence'`, MaxSafeInteger); err != nil {
		t.Fatal(err)
	}
	seq, err := outbox.Append(context.Background(), []byte("terminal"))
	if err != nil || seq != MaxSafeInteger {
		t.Fatalf("terminal Append() = %d, %v", seq, err)
	}
	if _, err = outbox.Append(context.Background(), []byte("overflow")); !errors.Is(err, ErrSequenceExhausted) {
		t.Fatalf("post-terminal Append() error = %v, want ErrSequenceExhausted", err)
	}
	status, err := outbox.Status(context.Background())
	if err != nil || !status.SequenceExhausted || status.NextSequence != MaxSafeInteger {
		t.Fatalf("terminal status = %#v, %v", status, err)
	}
	if err = outbox.Close(); err != nil {
		t.Fatal(err)
	}
	outbox, err = OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	status, err = outbox.Status(context.Background())
	if err != nil || !status.SequenceExhausted || status.NextSequence != MaxSafeInteger {
		t.Fatalf("restart terminal status = %#v, %v", status, err)
	}
}

func TestOutboxAppendCancellationAtCommitBoundaryRollsBack(t *testing.T) {
	outbox, err := OpenOutbox(context.Background(), filepath.Join(t.TempDir(), "outbox.db"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	outbox.testHooks.beforeAppendCommit = func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, appendErr := outbox.Append(ctx, []byte("blocked"))
		result <- appendErr
	}()
	<-entered
	cancel()
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Append() error = %v, want context.Canceled", err)
	}
	items, err := outbox.List(context.Background(), 10, 1024)
	if err != nil || len(items) != 0 {
		t.Fatalf("canceled append committed: %#v, %v", items, err)
	}
	status, err := outbox.Status(context.Background())
	if err != nil || status.NextSequence != 1 {
		t.Fatalf("canceled append consumed sequence: %#v, %v", status, err)
	}
}

func TestOutboxAckCancellationAtCommitBoundaryRollsBack(t *testing.T) {
	outbox, err := OpenOutbox(context.Background(), filepath.Join(t.TempDir(), "outbox.db"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	for i := 0; i < 2; i++ {
		if _, err = outbox.Append(context.Background(), []byte("payload")); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{})
	outbox.testHooks.beforeAckCommit = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- outbox.Acknowledge(ctx, 1) }()
	<-entered
	cancel()
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Acknowledge() error = %v, want context.Canceled", err)
	}
	items, err := outbox.List(context.Background(), 10, 1024)
	if err != nil || len(items) != 2 {
		t.Fatalf("canceled ACK compacted: %#v, %v", items, err)
	}
}

func TestCGODisabledRuntimeMarker(t *testing.T) {
	// The verification command runs this test with CGO_ENABLED=0. Keep a
	// runtime operation here so a compile-only stub driver cannot pass.
	if os.Getenv("CGO_ENABLED") == "0" {
		path := filepath.Join(t.TempDir(), "cgo-disabled.db")
		outbox, err := OpenOutbox(context.Background(), path, 16<<20)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = outbox.Append(context.Background(), []byte("cgo-free")); err != nil {
			t.Fatal(err)
		}
		if err = outbox.Close(); err != nil {
			t.Fatal(err)
		}
		outbox, err = OpenOutbox(context.Background(), path, 16<<20)
		if err != nil {
			t.Fatal(err)
		}
		defer outbox.Close()
		items, err := outbox.List(context.Background(), 1, 1024)
		if err != nil || len(items) != 1 || !strings.Contains(string(items[0].Payload), "cgo-free") {
			t.Fatalf("CGO-disabled restart = %#v, %v", items, err)
		}
	}
}

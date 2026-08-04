package keeperexport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOutboxRestartSequenceRawBytesAndPartialAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	ctx := context.Background()
	outbox, err := OpenOutbox(ctx, path, 16<<20)
	if err != nil {
		t.Fatalf("OpenOutbox() error = %v", err)
	}
	streamID := outbox.StreamID()
	first := []byte(`{"request_id":"duplicate","spacing": [1, 2]}`)
	second := []byte("{\n  \"request_id\": \"duplicate\",\n  \"spacing\": [1,2]\n}")
	seq1, err := outbox.Append(ctx, first)
	if err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	seq2, err := outbox.Append(ctx, second)
	if err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}
	if seq1 != 1 || seq2 != 2 {
		t.Fatalf("sequences = %d,%d, want 1,2", seq1, seq2)
	}
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}

	outbox, err = OpenOutbox(ctx, path, 16<<20)
	if err != nil {
		t.Fatalf("restart OpenOutbox() error = %v", err)
	}
	defer outbox.Close()
	if outbox.StreamID() != streamID {
		t.Fatalf("stream ID changed across restart: %q -> %q", streamID, outbox.StreamID())
	}
	items, err := outbox.List(ctx, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !bytes.Equal(items[0].Payload, first) || !bytes.Equal(items[1].Payload, second) {
		t.Fatalf("restart items lost exact bytes: %#v", items)
	}
	if err := outbox.Acknowledge(ctx, 1); err != nil {
		t.Fatalf("Acknowledge(1) error = %v", err)
	}
	items, err = outbox.List(ctx, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Sequence != 2 || !bytes.Equal(items[0].Payload, second) {
		t.Fatalf("partial ACK compacted wrong range: %#v", items)
	}
	status, err := outbox.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("manual QA status after restart/partial ACK: stream=%s next=%d ack=%d backlog_events=%d backlog_bytes=%d", status.StreamID, status.NextSequence, status.AcknowledgedThrough, status.BacklogEvents, status.BacklogBytes)
	if status.StreamID != streamID || status.NextSequence != 3 || status.AcknowledgedThrough != 1 || status.BacklogEvents != 1 || status.BacklogBytes != int64(len(second)) {
		t.Fatalf("status = %#v", status)
	}
}

func TestOutboxPersistsImmutableInstanceBindingAndFingerprintSecret(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outbox.db")
	outbox, err := OpenOutbox(ctx, path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	instanceID := "0198aa10-4d88-7a20-8f4e-8c8de4a9cb11"
	secret, err := outbox.BindInstance(ctx, instanceID)
	if err != nil {
		t.Fatalf("BindInstance() error = %v", err)
	}
	if len(secret) != 32 {
		t.Fatalf("fingerprint secret length = %d, want 32", len(secret))
	}
	secret[0] ^= 0xff
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("outbox permissions = %v, %v; want owner-only", info, err)
	}

	outbox, err = OpenOutbox(ctx, path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	restartedSecret, err := outbox.BindInstance(ctx, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(secret, restartedSecret) || len(restartedSecret) != 32 {
		t.Fatalf("returned secret was not defensively copied or persisted")
	}
	if _, err := outbox.BindInstance(ctx, "0198aa10-4d88-7a20-8f4e-8c8de4a9cb12"); !errors.Is(err, ErrInstanceBindingMismatch) {
		t.Fatalf("BindInstance(different) error = %v, want ErrInstanceBindingMismatch", err)
	}
}

func TestOutboxQuotaAndFailedAppendDoesNotConsumeSequence(t *testing.T) {
	ctx := context.Background()
	outbox, err := OpenOutbox(ctx, filepath.Join(t.TempDir(), "outbox.db"), 64)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	if _, err := outbox.Append(ctx, bytes.Repeat([]byte("a"), 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Append(ctx, bytes.Repeat([]byte("b"), 40)); !errors.Is(err, ErrOutboxFull) {
		t.Fatalf("second Append() error = %v, want ErrOutboxFull", err)
	}
	status, err := outbox.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastError == "" {
		t.Fatal("outbox full failure was not observable in status")
	}
	if err := outbox.Acknowledge(ctx, 1); err != nil {
		t.Fatal(err)
	}
	seq, err := outbox.Append(ctx, []byte("after-ack"))
	if err != nil {
		t.Fatal(err)
	}
	if seq != 2 {
		t.Fatalf("sequence after failed append = %d, want 2", seq)
	}
}

func TestOutboxRejectsCorruptAndNonWritablePaths(t *testing.T) {
	ctx := context.Background()
	t.Run("corrupt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "outbox.db")
		if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenOutbox(ctx, path, 16<<20); err == nil {
			t.Fatal("OpenOutbox(corrupt) error = nil")
		}
	})
	t.Run("parent is file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "parent")
		if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenOutbox(ctx, filepath.Join(parent, "outbox.db"), 16<<20); err == nil {
			t.Fatal("OpenOutbox(parent file) error = nil")
		}
	})
	t.Run("read only file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "outbox.db")
		outbox, err := OpenOutbox(ctx, path, 16<<20)
		if err != nil {
			t.Fatal(err)
		}
		if err = outbox.Close(); err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		if _, err = OpenOutbox(ctx, path, 16<<20); err == nil {
			t.Fatal("OpenOutbox(read only) error = nil")
		}
	})
}

func TestOutboxConcurrentAppendIsMonotonic(t *testing.T) {
	ctx := context.Background()
	outbox, err := OpenOutbox(ctx, filepath.Join(t.TempDir(), "outbox.db"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	const count = 64
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errAppend := outbox.Append(ctx, []byte(`{"request_id":"duplicate"}`))
			errs <- errAppend
		}()
	}
	wg.Wait()
	close(errs)
	for errAppend := range errs {
		if errAppend != nil {
			t.Fatalf("concurrent Append() error = %v", errAppend)
		}
	}
	items, err := outbox.List(ctx, count, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != count {
		t.Fatalf("items = %d, want %d", len(items), count)
	}
	for i, item := range items {
		if item.Sequence != int64(i+1) {
			t.Fatalf("items[%d].Sequence = %d, want %d", i, item.Sequence, i+1)
		}
	}
}

func TestOutboxInvalidAndInterruptedAckPreserveBacklog(t *testing.T) {
	ctx := context.Background()
	outbox, err := OpenOutbox(ctx, filepath.Join(t.TempDir(), "outbox.db"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	for i := 0; i < 3; i++ {
		if _, err = outbox.Append(ctx, []byte("payload")); err != nil {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = outbox.Acknowledge(canceled, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acknowledge(canceled) error = %v, want context.Canceled", err)
	}
	if err = outbox.Acknowledge(ctx, 4); err == nil {
		t.Fatal("Acknowledge(future) error = nil")
	}
	items, err := outbox.List(ctx, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("interrupted/invalid ACK compacted backlog: %d items", len(items))
	}
	if err = outbox.Acknowledge(ctx, 2); err != nil {
		t.Fatal(err)
	}
	items, err = outbox.List(ctx, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Sequence != 3 {
		t.Fatalf("valid ACK result = %#v", items)
	}
}

func TestOutboxOperationsHonorCanceledContext(t *testing.T) {
	outbox, err := OpenOutbox(context.Background(), filepath.Join(t.TempDir(), "outbox.db"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := outbox.Append(ctx, []byte("payload")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append(canceled) error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Append(canceled) blocked for %s", elapsed)
	}
}

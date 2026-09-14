package executor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestDevinDumpRejectedRequestDisabledByDefault pins that diagnostics stay off
// unless the operator opts in: a shared host must not accumulate request
// payloads on disk just because devin rejected something.
func TestDevinDumpRejectedRequestDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(devinDumpDirEnv, "")
	devinDumpCount.Store(0)

	devinDumpRejectedRequest([]byte(`{"model":"swe-2-high"}`), errors.New("invalid_argument"))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote %d files with the dump disabled", len(entries))
	}
}

// TestDevinDumpRejectedRequestWritesPayload pins that the opt-in path captures
// the exact bytes, which is the whole point: the rejection carries no body and
// the logged shape is only a summary.
func TestDevinDumpRejectedRequestWritesPayload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dumps")
	t.Setenv(devinDumpDirEnv, dir)
	devinDumpCount.Store(0)

	payload := []byte(`{"model":"swe-2-high","tools":[{"type":"function"}]}`)
	devinDumpRejectedRequest(payload, errors.New("invalid_argument"))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d files, want 1", len(entries))
	}
	got, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("dump = %q, want the exact request bytes", got)
	}
}

// TestDevinDumpRejectedRequestStopsAtLimit pins the cap so enabling the dump on
// a busy host cannot fill the disk.
func TestDevinDumpRejectedRequestStopsAtLimit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dumps")
	t.Setenv(devinDumpDirEnv, dir)
	devinDumpCount.Store(0)

	for i := 0; i < devinDumpLimit+5; i++ {
		devinDumpRejectedRequest([]byte(`{"n":1}`), errors.New("invalid_argument"))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != devinDumpLimit {
		t.Fatalf("wrote %d files, want the %d cap", len(entries), devinDumpLimit)
	}
}

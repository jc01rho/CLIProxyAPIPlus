package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupStaleBodySourcesRemovesOnlyStalePartDirs(t *testing.T) {
	baseDir := t.TempDir()

	staleSource, errStale := NewFileBodySourceInDir(baseDir, "request-body")
	if errStale != nil {
		t.Fatalf("create stale source: %v", errStale)
	}
	if _, errPart := staleSource.CreatePart("body"); errPart != nil {
		t.Fatalf("create stale part: %v", errPart)
	}
	freshSource, errFresh := NewFileBodySourceInDir(baseDir, "request-body")
	if errFresh != nil {
		t.Fatalf("create fresh source: %v", errFresh)
	}
	if _, errPart := freshSource.CreatePart("body"); errPart != nil {
		t.Fatalf("create fresh part: %v", errPart)
	}
	unrelatedDir := filepath.Join(baseDir, "keep-me")
	if errMkdir := os.MkdirAll(unrelatedDir, 0755); errMkdir != nil {
		t.Fatalf("create unrelated dir: %v", errMkdir)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if errTime := os.Chtimes(staleSource.dir, oldTime, oldTime); errTime != nil {
		t.Fatalf("age stale part dir: %v", errTime)
	}

	if errCleanup := cleanupStaleBodySources(baseDir, time.Hour); errCleanup != nil {
		t.Fatalf("cleanup stale body sources: %v", errCleanup)
	}

	if _, errStat := os.Stat(staleSource.dir); !os.IsNotExist(errStat) {
		t.Fatalf("stale part dir still present: %v", errStat)
	}
	if _, errStat := os.Stat(freshSource.dir); errStat != nil {
		t.Fatalf("fresh part dir was removed: %v", errStat)
	}
	if _, errStat := os.Stat(unrelatedDir); errStat != nil {
		t.Fatalf("unrelated dir was removed: %v", errStat)
	}
}

func TestCleanupStaleBodySourcesIgnoresMissingDir(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "absent")
	if errCleanup := cleanupStaleBodySources(missingDir, time.Hour); errCleanup != nil {
		t.Fatalf("cleanup on missing dir = %v, want nil", errCleanup)
	}
}

func TestNewFileRequestLoggerRemovesOrphanedPartDirs(t *testing.T) {
	logsDir := t.TempDir()

	orphan, errOrphan := NewFileBodySourceInDir(logsDir, "request-body")
	if errOrphan != nil {
		t.Fatalf("create orphaned source: %v", errOrphan)
	}
	if _, errPart := orphan.CreatePart("body"); errPart != nil {
		t.Fatalf("create orphaned part: %v", errPart)
	}
	oldTime := time.Now().Add(-24 * time.Hour)
	if errTime := os.Chtimes(orphan.dir, oldTime, oldTime); errTime != nil {
		t.Fatalf("age orphaned part dir: %v", errTime)
	}

	_ = NewFileRequestLogger(false, logsDir, "", 10)

	if _, errStat := os.Stat(orphan.dir); !os.IsNotExist(errStat) {
		t.Fatalf("orphaned part dir still present after logger creation: %v", errStat)
	}
}

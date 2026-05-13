package logging

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func countMatchingFiles(t *testing.T, dir string, match func(string) bool) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir %s: %v", dir, err)
	}
	matches := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if match(entry.Name()) {
			matches = append(matches, entry.Name())
		}
	}
	return matches
}

func TestLogRequest_EnabledAndForce_WritesNormalAndErrorLogs(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 10)

	errLog := logger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"error":"upstream failure","detail":"full body"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"req-enabled-force",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("LogRequestWithOptions error: %v", errLog)
	}

	normalLogs := countMatchingFiles(t, logsDir, func(name string) bool {
		return strings.HasSuffix(name, "req-enabled-force.log") && !strings.HasPrefix(name, "error-")
	})
	errorLogs := countMatchingFiles(t, logsDir, func(name string) bool {
		return strings.HasPrefix(name, "error-") && strings.HasSuffix(name, "req-enabled-force.log")
	})

	if len(normalLogs) != 1 {
		t.Fatalf("normal logs = %v, want 1 file", normalLogs)
	}
	if len(errorLogs) != 1 {
		t.Fatalf("error logs = %v, want 1 file", errorLogs)
	}

	errorContent, errRead := os.ReadFile(filepath.Join(logsDir, errorLogs[0]))
	if errRead != nil {
		t.Fatalf("read error log: %v", errRead)
	}
	if !strings.Contains(string(errorContent), `{"error":"upstream failure","detail":"full body"}`) {
		t.Fatalf("error log missing full error body: %s", string(errorContent))
	}
}

func TestLogRequest_DisabledAndForce_WritesOnlyErrorLog(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(false, logsDir, "", 10)

	errLog := logger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"req-disabled-force",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("LogRequestWithOptions error: %v", errLog)
	}

	normalLogs := countMatchingFiles(t, logsDir, func(name string) bool {
		return strings.HasSuffix(name, "req-disabled-force.log") && !strings.HasPrefix(name, "error-")
	})
	errorLogs := countMatchingFiles(t, logsDir, func(name string) bool {
		return strings.HasPrefix(name, "error-") && strings.HasSuffix(name, "req-disabled-force.log")
	})

	if len(normalLogs) != 0 {
		t.Fatalf("normal logs = %v, want none", normalLogs)
	}
	if len(errorLogs) != 1 {
		t.Fatalf("error logs = %v, want 1 file", errorLogs)
	}
}

func TestLogRequest_ForceCleanupKeepsLatestTenErrorLogs(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 10)

	for i := 0; i < 12; i++ {
		requestID := "req-cleanup-" + time.Now().Add(time.Duration(i)*time.Millisecond).Format("150405.000000000")
		errLog := logger.LogRequestWithOptions(
			"/v1/chat/completions",
			http.MethodPost,
			map[string][]string{"Content-Type": {"application/json"}},
			[]byte(`{"input":"hello"}`),
			http.StatusBadGateway,
			map[string][]string{"Content-Type": {"application/json"}},
			[]byte(`{"error":"upstream failure"}`),
			nil,
			nil,
			nil,
			nil,
			nil,
			true,
			requestID,
			time.Now(),
			time.Now(),
		)
		if errLog != nil {
			t.Fatalf("LogRequestWithOptions error at iteration %d: %v", i, errLog)
		}
		time.Sleep(5 * time.Millisecond)
	}

	errorLogs := countMatchingFiles(t, logsDir, func(name string) bool {
		return strings.HasPrefix(name, "error-") && strings.HasSuffix(name, ".log")
	})
	if len(errorLogs) != 10 {
		t.Fatalf("error log count = %d, want 10; files=%v", len(errorLogs), errorLogs)
	}
}

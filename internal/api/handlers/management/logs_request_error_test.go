package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func writeTestErrorLog(t *testing.T, dir, name string, modTime time.Time) {
	t.Helper()
	fullPath := filepath.Join(dir, name)
	if err := os.WriteFile(fullPath, []byte("error log body"), 0o644); err != nil {
		t.Fatalf("failed to write test error log %s: %v", fullPath, err)
	}
	if err := os.Chtimes(fullPath, modTime, modTime); err != nil {
		t.Fatalf("failed to set modtime for %s: %v", fullPath, err)
	}
}

func TestGetRequestErrorLogs_ReturnsFilesEvenWhenRequestLogEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	writeTestErrorLog(t, logsDir, "error-v1-chat-completions-old.log", older)
	writeTestErrorLog(t, logsDir, "error-v1-chat-completions-new.log", newer)

	h := &Handler{
		cfg:    &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}},
		logDir: logsDir,
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)

	h.GetRequestErrorLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Files) != 2 {
		t.Fatalf("files len = %d, want 2; body=%s", len(payload.Files), rec.Body.String())
	}
	if payload.Files[0].Name != "error-v1-chat-completions-new.log" {
		t.Fatalf("files[0] = %q, want newest file first", payload.Files[0].Name)
	}
	if payload.Files[1].Name != "error-v1-chat-completions-old.log" {
		t.Fatalf("files[1] = %q, want oldest file second", payload.Files[1].Name)
	}
}

package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func putAPIKeys(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := setupTestRouter(h)
	router.PUT("/api-keys", h.PutAPIKeys)
	req := httptest.NewRequest(http.MethodPut, "/api-keys", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestPersistRefusesToOverwriteUnreloadedExternalEdit(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configFile, []byte("api-keys:\n  - a\nrequest-retry: 3\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	cfg, errLoad := config.LoadConfig(configFile)
	if errLoad != nil {
		t.Fatalf("LoadConfig: %v", errLoad)
	}
	h := NewHandler(cfg, configFile, nil)
	// A manual edit lands on disk before the watcher reloads it into h.cfg.
	if errWrite := os.WriteFile(configFile, []byte("api-keys:\n  - a\nrequest-retry: 5\n"), 0o600); errWrite != nil {
		t.Fatalf("external edit: %v", errWrite)
	}

	rec := putAPIKeys(t, h, `["a","b"]`)

	reloaded, errReload := config.LoadConfig(configFile)
	if errReload != nil {
		t.Fatalf("LoadConfig after save: %v", errReload)
	}
	if reloaded.RequestRetry != 5 {
		t.Fatalf("external edit lost: request-retry=%d, want 5 (PUT status=%d)", reloaded.RequestRetry, rec.Code)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT status=%d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}
}

func TestPersistAcceptsConsecutiveSaves(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	// The plaintext secret makes LoadConfig rewrite the file it just parsed.
	if errWrite := os.WriteFile(configFile, []byte("remote-management:\n  secret-key: plain-secret\napi-keys:\n  - a\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	cfg, errLoad := config.LoadConfig(configFile)
	if errLoad != nil {
		t.Fatalf("LoadConfig: %v", errLoad)
	}
	h := NewHandler(cfg, configFile, nil)
	for _, body := range []string{`["a","b"]`, `["a","b","c"]`} {
		if rec := putAPIKeys(t, h, body); rec.Code != http.StatusOK {
			t.Fatalf("PUT %s status=%d body=%s, want 200", body, rec.Code, rec.Body.String())
		}
	}
	reloaded, errReload := config.LoadConfig(configFile)
	if errReload != nil {
		t.Fatalf("LoadConfig after saves: %v", errReload)
	}
	if got := len(reloaded.APIKeys); got != 3 {
		t.Fatalf("api-keys=%v, want 3 keys", reloaded.APIKeys)
	}
}

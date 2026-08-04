package management

import (
	"bytes"
	"compress/gzip"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestUsageExportConnectionTestRejectsGzippedIdentity(t *testing.T) {
	const gzipMarker = "REMOTE_GZIP_SECRET_MARKER_a9f2"
	keeper := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := []byte(`{"protocolVersion":"keeper-export/v1","instance":{"instanceId":"0198aa10-4d88-7a20-8f4e-8c8de4a9cb12","displayName":"gzipped"},"credential":{"credentialId":"0198aa11-1055-7f12-8a00-e843d1e17522","scopes":["identity:test"]},"serverTime":"2026-08-03T12:35:00.000Z"}`)
		if strings.Contains(string(body), gzipMarker) {
			t.Fatalf("fixture leaked marker")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := gw.Close(); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(buf.Bytes())
	}))
	defer keeper.Close()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "gzip-ca.pem")
	cert := keeper.Certificate()
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("usage-statistics-enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cfg, configPath, nil)
	r := setupTestRouter(h)
	r.POST("/test", h.TestUsageExportConnection)
	r.GET("/status", h.GetUsageExportStatus)
	t.Setenv("GZIP_HANDLER_TOKEN", "keeper-token")
	outboxPath := filepath.Join(dir, "must-not-open.db")
	body := validUsageExportTestBody(keeper.URL, "GZIP_HANDLER_TOKEN", outboxPath, &caPath)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), `"code":"keeper_invalid_response"`) {
		t.Fatalf("expected 502 keeper_invalid_response, got %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), gzipMarker) {
		t.Fatalf("test response leaked marker: %s", rr.Body.String())
	}
	statusRR := httptest.NewRecorder()
	r.ServeHTTP(statusRR, httptest.NewRequest(http.MethodGet, "/status", nil))
	if strings.Contains(statusRR.Body.String(), gzipMarker) {
		t.Fatalf("status leaked marker: %s", statusRR.Body.String())
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("connection test mutated config")
	}
	if _, err := os.Stat(outboxPath); !os.IsNotExist(err) {
		t.Fatalf("connection test mutated outbox: %v", err)
	}
}

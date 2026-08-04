package keeperexport_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"
)

const gzipIdentityMarker = "REMOTE_GZIP_SECRET_MARKER"

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newGzippedIdentityKeeper(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	const instanceID = "0198aa10-4d88-7a20-8f4e-8c8de4a9cb12"
	const credentialID = "0198aa11-1055-7f12-8a00-e843d1e17522"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/export/identity" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		body := []byte(`{"protocolVersion":"keeper-export/v1","instance":{"instanceId":"` + instanceID + `","displayName":"gzipped instance"},"credential":{"credentialId":"` + credentialID + `","scopes":["identity:test"]},"serverTime":"2026-08-03T12:35:00.000Z"}`)
		if strings.Contains(string(body), gzipIdentityMarker) {
			t.Fatalf("fixture leaked marker before compression: %s", body)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		if _, err := w.Write(gzipBytes(t, body)); err != nil {
			t.Fatal(err)
		}
	})
	server := httptest.NewTLSServer(handler)
	caPath := filepath.Join(dir, "gzip-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return server
}

func writeGzipCA(t *testing.T, dir, name string, server *httptest.Server) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKeeperConnectionTestRejectsGzippedIdentity(t *testing.T) {
	dir := t.TempDir()
	keeper := newGzippedIdentityKeeper(t, dir)
	defer keeper.Close()
	caPath := writeGzipCA(t, dir, "gzip-ca.pem", keeper)
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled, cfg.Mode = true, config.UsageExportModePush
	cfg.Keeper.URL = keeper.URL
	cfg.Keeper.TokenEnv = "GZIP_TEST_TOKEN"
	cfg.Keeper.CAFile = &caPath
	cfg.Outbox.Path = filepath.Join(dir, "gzip-test.db")
	cfg.Outbox.MaxBytes = 16 << 20
	cfg.Metadata.Enabled = false
	t.Setenv(cfg.Keeper.TokenEnv, "token")
	cfgPath := filepath.Join(dir, "test-cfg.yaml")
	if err := os.WriteFile(cfgPath, []byte("dummy: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgBytesBefore, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keeperexport.TestConnection(context.Background(), cfg); err == nil {
		t.Fatal("gzipped identity was accepted")
	} else if err.Code != "keeper_invalid_response" {
		t.Fatalf("expected keeper_invalid_response got code=%s err=%v", err.Code, err)
	}
	if _, err := os.Stat(cfgPath); err != nil || cfgBytesBefore.Size() == 0 {
		t.Fatalf("config file mutated: before=%v after=%v", cfgBytesBefore, err)
	}
	if _, err := os.Stat(cfg.Outbox.Path); err == nil {
		t.Fatalf("outbox file was created at %s", cfg.Outbox.Path)
	}
}

func TestKeeperClientPreservesCAAndDisablesCompression(t *testing.T) {
	dir := t.TempDir()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") == "gzip" {
			t.Fatalf("client signaled Accept-Encoding=gzip to upstream")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"protocolVersion":"keeper-export/v1","instance":{"instanceId":"0198aa10-4d88-7a20-8f4e-8c8de4a9cb12","displayName":"plain"},"credential":{"credentialId":"0198aa11-1055-7f12-8a00-e843d1e17522","scopes":["identity:test"]},"serverTime":"2026-08-03T12:35:00.000Z"}`))
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	caPath := writeGzipCA(t, dir, "plain-ca.pem", server)
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled, cfg.Mode = true, config.UsageExportModePush
	cfg.Keeper.URL = server.URL
	cfg.Keeper.TokenEnv = "COMPRESSION_TEST_TOKEN"
	cfg.Keeper.CAFile = &caPath
	cfg.Outbox.Path = filepath.Join(dir, "compression-test.db")
	cfg.Outbox.MaxBytes = 16 << 20
	cfg.Metadata.Enabled = false
	t.Setenv(cfg.Keeper.TokenEnv, "token")
	resp, err := keeperexport.TestConnection(context.Background(), cfg)
	if err != nil {
		t.Fatalf("expected plain identity accepted, got err=%v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got %+v", resp)
	}
}

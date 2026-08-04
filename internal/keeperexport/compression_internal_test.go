package keeperexport

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
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const gzipIdentityMarker = "REMOTE_GZIP_SECRET_MARKER"

func newInternalGzippedIdentityKeeper(t *testing.T) *httptest.Server {
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
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := gw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(buf.Bytes()); err != nil {
			t.Fatal(err)
		}
	})
	return httptest.NewTLSServer(handler)
}

func TestWorkerRejectsGzippedIdentity(t *testing.T) {
	dir := t.TempDir()
	keeper := newInternalGzippedIdentityKeeper(t)
	defer keeper.Close()
	caPath := filepath.Join(dir, "gzip-worker-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: keeper.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled, cfg.Mode = true, config.UsageExportModePush
	cfg.Keeper.URL = keeper.URL
	cfg.Keeper.TokenEnv = "GZIP_WORKER_TOKEN"
	cfg.Keeper.CAFile = &caPath
	cfg.Outbox.Path = filepath.Join(dir, "gzip-worker.db")
	cfg.Outbox.MaxBytes = 16 << 20
	cfg.Metadata.Enabled = false
	t.Setenv(cfg.Keeper.TokenEnv, "token")
	var runtime Runtime
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	changed := make(chan struct{}, 1)
	runtime.mu.RLock()
	worker := runtime.worker
	runtime.mu.RUnlock()
	worker.setStatusHook(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	defer worker.setStatusHook(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var status StatusResponse
	for {
		status, _ = runtime.ManagementStatus(ctx)
		if status.LastError != nil {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("worker did not surface identity error: %#v", status)
		}
	}
	if status.LastError == nil || status.LastError.Code != "keeper_invalid_response" {
		t.Fatalf("expected worker keeper_invalid_response got %#v", status.LastError)
	}
	encoded, err := MarshalStatusResponse(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), gzipIdentityMarker) {
		t.Fatalf("worker status leaked remote marker: %s", encoded)
	}
}

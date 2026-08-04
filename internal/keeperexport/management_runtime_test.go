package keeperexport

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestManagementStatusRetryingUsesSanitizedRemoteError(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultUsageExportConfig(dir)
	var runtime Runtime
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	disabled, err := runtime.ManagementStatus(context.Background())
	if err != nil || disabled.State != StateDisabled {
		t.Fatalf("disabled=%#v err=%v", disabled, err)
	}

	identitySeen := make(chan struct{}, 1)
	releaseFailure := make(chan struct{})
	keeper := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		identitySeen <- struct{}{}
		<-releaseFailure
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"protocolVersion":"keeper-export/v1","error":{"code":"service_unavailable","message":"REMOTE_SECRET_MARKER retry detail","retryable":true}}`))
	}))
	defer keeper.Close()
	caPath := writeManagementTestCA(t, dir, "retry-ca.pem", keeper)
	cfg.Enabled, cfg.Mode = true, config.UsageExportModePush
	cfg.Keeper.URL, cfg.Keeper.TokenEnv, cfg.Keeper.CAFile = keeper.URL, "STATUS_KEEPER_TOKEN", &caPath
	cfg.Outbox.Path, cfg.Outbox.MaxBytes = filepath.Join(dir, "status.db"), 16<<20
	cfg.Delivery.InitialBackoffMs, cfg.Delivery.MaxBackoffMs = 5000, 5000
	cfg.Metadata.Enabled = false
	t.Setenv(cfg.Keeper.TokenEnv, "token")
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	awaitManagementSignal(t, identitySeen, "identity attempt")

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
	close(releaseFailure)
	status := awaitManagementStatus(t, &runtime, changed, func(status StatusResponse) bool {
		return status.State == StateRetrying
	})
	if status.NextRetryAt == nil || status.LastError == nil || !status.LastError.Retryable {
		t.Fatalf("retrying status=%#v", status)
	}
	encoded, err := MarshalStatusResponse(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "REMOTE_SECRET_MARKER") {
		t.Fatalf("runtime status leaked remote marker: %s", encoded)
	}
}

func TestManagementStatusNextExpectedComesOnlyFromUsageAck(t *testing.T) {
	identitySeen := make(chan struct{}, 1)
	metadataSeen := make(chan struct{}, 3)
	usageAckSeen := make(chan struct{}, 1)
	keeper := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/export/identity":
			identitySeen <- struct{}{}
			fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","instance":{"instanceId":"%s","displayName":"connected instance"},"credential":{"credentialId":"%s","scopes":["usage:push","metadata:push","identity:test"]},"serverTime":"2026-08-03T12:35:00.000Z"}`, testInstanceID, testCredentialID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/export/usage-batches":
			var batch struct {
				StreamID string `json:"streamId"`
				Events   []struct {
					Sequence int64 `json:"sequence"`
				} `json:"events"`
			}
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			through := batch.Events[len(batch.Events)-1].Sequence
			usageAckSeen <- struct{}{}
			fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","streamId":%q,"acknowledgedThrough":%d,"nextExpectedSequence":%d,"acceptedCount":%d,"replayedCount":0}`, batch.StreamID, through, through+1, len(batch.Events))
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/export/metadata/"):
			var payload struct {
				Revision int64             `json:"revision"`
				Items    []json.RawMessage `json:"items"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			category := strings.TrimPrefix(r.URL.Path, "/api/v1/export/metadata/")
			metadataSeen <- struct{}{}
			fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","category":%q,"revision":%d,"applied":true,"itemCount":%d,"serverTime":"2026-08-03T12:36:01.000Z"}`, category, payload.Revision, len(payload.Items))
		default:
			http.NotFound(w, r)
		}
	}))
	defer keeper.Close()
	dir := t.TempDir()
	caPath := writeManagementTestCA(t, dir, "connected-ca.pem", keeper)
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled, cfg.Mode = true, config.UsageExportModePush
	cfg.Keeper.URL, cfg.Keeper.TokenEnv, cfg.Keeper.CAFile = keeper.URL, "CONNECTED_KEEPER_TOKEN", &caPath
	cfg.Outbox.MaxBytes = 16 << 20
	cfg.Delivery.FlushIntervalMs = 100
	cfg.Delivery.InitialBackoffMs = 100
	cfg.Delivery.MaxBackoffMs = 100
	t.Setenv(cfg.Keeper.TokenEnv, "token")
	var runtime Runtime
	runtime.SetSnapshotSource(func() SnapshotInput { return SnapshotInput{} })
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	awaitManagementSignal(t, identitySeen, "identity")
	for range 3 {
		awaitManagementSignal(t, metadataSeen, "metadata")
	}
	preAck, err := runtime.ManagementStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if preAck.NextExpectedSequence != nil {
		t.Fatalf("identity/metadata synthesized nextExpectedSequence: %#v", preAck)
	}

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
	if err := runtime.Append(context.Background(), []byte(`{"request_id":"status-ack"}`)); err != nil {
		t.Fatal(err)
	}
	status := awaitManagementStatus(t, &runtime, changed, func(status StatusResponse) bool {
		return status.State == StateConnected && status.NextExpectedSequence != nil
	})
	select {
	case <-usageAckSeen:
	default:
		t.Fatal("status recorded Keeper ACK before fake Keeper sent it")
	}
	if status.NextExpectedSequence == nil || *status.NextExpectedSequence != 2 {
		t.Fatalf("connected status=%#v", status)
	}
}

func writeManagementTestCA(t *testing.T, dir, name string, server *httptest.Server) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func awaitManagementSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out awaiting %s", name)
	}
}

func awaitManagementStatus(t *testing.T, runtime *Runtime, changed <-chan struct{}, match func(StatusResponse) bool) StatusResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		status, err := runtime.ManagementStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if match(status) {
			return status
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("timed out awaiting runtime status; last=%#v", status)
		}
	}
}

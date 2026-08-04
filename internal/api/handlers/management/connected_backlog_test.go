package management

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"
)

// TestUsageExportStatusHandlerConnectedWithPendingBacklog proves a real
// identity-bound runtime holding one pending usage event and no error reports
// HTTP 200 connected through the production status handler and strict-decodes.
func TestUsageExportStatusHandlerConnectedWithPendingBacklog(t *testing.T) {
	usageAttemptSeen := make(chan struct{}, 1)
	release := make(chan struct{})
	keeper := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/export/identity":
			fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","instance":{"instanceId":"%s","displayName":"connected instance"},"credential":{"credentialId":"0198aa10-4d88-7a20-8f4e-8c8de4a9cb12","scopes":["usage:push","identity:test"]},"serverTime":"2026-08-03T12:35:00.000Z"}`, managementTestInstanceID)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/export/usage-batches":
			usageAttemptSeen <- struct{}{}
			<-release
			var batch struct {
				StreamID string `json:"streamId"`
				Events   []struct {
					Sequence int64 `json:"sequence"`
				} `json:"events"`
			}
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch.Events) == 0 {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			through := batch.Events[len(batch.Events)-1].Sequence
			fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","streamId":%q,"acknowledgedThrough":%d,"nextExpectedSequence":%d,"acceptedCount":%d,"replayedCount":0}`, batch.StreamID, through, through+1, len(batch.Events))
		default:
			http.NotFound(w, r)
		}
	}))
	defer keeper.Close()
	defer close(release)
	dir := t.TempDir()
	caPath := filepath.Join(dir, "pending-backlog-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: keeper.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled, cfg.Mode = true, config.UsageExportModePush
	cfg.Keeper.URL, cfg.Keeper.TokenEnv, cfg.Keeper.CAFile = keeper.URL, "PENDING_BACKLOG_TOKEN", &caPath
	cfg.Outbox.MaxBytes, cfg.Metadata.Enabled = 16<<20, false
	t.Setenv(cfg.Keeper.TokenEnv, "token")
	var runtime keeperexport.Runtime
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.Append(context.Background(), []byte(`{"request_id":"pending-one"}`)); err != nil {
		t.Fatal(err)
	}
	// The worker is single-threaded: identity success is recorded before the
	// usage POST, and the POST blocks on release, so at this signal the event
	// is pending with no error and no scheduled retry.
	select {
	case <-usageAttemptSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out awaiting pending usage attempt")
	}
	h := NewHandler(&config.Config{}, "", nil)
	h.SetUsageExportRuntime(&runtime)
	r := setupTestRouter(h)
	r.GET("/status", h.GetUsageExportStatus)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 connected with pending backlog, got %d body=%s", rr.Code, rr.Body.String())
	}
	decoded, perr := keeperexport.DecodeStatusResponse(rr.Body.Bytes())
	if perr != nil {
		t.Fatalf("handler response failed strict decode: %v body=%s", perr, rr.Body.String())
	}
	if decoded.State != keeperexport.StateConnected || decoded.Instance == nil ||
		decoded.BacklogEvents != 1 || decoded.BacklogBytes <= 0 || decoded.OldestBacklogAt == nil ||
		decoded.LastError != nil || decoded.NextRetryAt != nil {
		t.Fatalf("connected pending-backlog status=%#v", decoded)
	}
}

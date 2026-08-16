package keeperexport

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	testInstanceID   = "0198aa10-4d88-7a20-8f4e-8c8de4a9cb11"
	testCredentialID = "0198aa10-4d88-7a20-8f4e-8c8de4a9cb12"
)

type fakeKeeper struct {
	mu                      sync.Mutex
	usageBodies             [][]byte
	usageSequences          []int64
	metadataBodies          map[string][][]byte
	metadataRevisions       map[string][]int64
	staleRevisionFrom       atomic.Int64
	keeperCurrentRevision   atomic.Int64
	identitySeen            chan struct{}
	usageSeen               chan struct{}
	metadataSeen            chan struct{}
	disconnectOnce          atomic.Bool
	invalidAck              atomic.Bool
	conflictingMetadataOnce atomic.Bool
	expectedToken           atomic.Value
}

func newFakeKeeper() *fakeKeeper {
	f := &fakeKeeper{identitySeen: make(chan struct{}, 16), usageSeen: make(chan struct{}, 16), metadataSeen: make(chan struct{}, 16), metadataBodies: make(map[string][][]byte), metadataRevisions: make(map[string][]int64)}
	f.expectedToken.Store("token-one")
	return f
}

func (f *fakeKeeper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.expectedToken.Load().(string) {
		writeTestError(w, "invalid_credential")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/export/identity":
		select {
		case f.identitySeen <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","instance":{"instanceId":"%s","displayName":"test instance"},"credential":{"credentialId":"%s","scopes":["usage:push","metadata:push","identity:test"]},"serverTime":"2026-08-03T12:35:00.000Z"}`, testInstanceID, testCredentialID)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/export/usage-batches":
		body := mustReadRequest(tReader{r})
		batch, perr := DecodeUsageBatch(body)
		if perr != nil {
			writeTestError(w, perr.Code)
			return
		}
		f.mu.Lock()
		f.usageBodies = append(f.usageBodies, append([]byte(nil), body...))
		for _, event := range batch.Events {
			f.usageSequences = append(f.usageSequences, event.Sequence)
		}
		f.mu.Unlock()
		select {
		case f.usageSeen <- struct{}{}:
		default:
		}
		if f.disconnectOnce.CompareAndSwap(true, false) {
			hijacker := w.(http.Hijacker)
			conn, _, _ := hijacker.Hijack()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if f.invalidAck.Load() {
			fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","streamId":"%s","acknowledgedThrough":1,"nextExpectedSequence":9,"acceptedCount":1,"replayedCount":0}`, batch.StreamID)
			return
		}
		through := batch.Events[len(batch.Events)-1].Sequence
		fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","streamId":"%s","acknowledgedThrough":%d,"nextExpectedSequence":%d,"acceptedCount":%d,"replayedCount":0}`, batch.StreamID, through, through+1, len(batch.Events))
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/export/metadata/"):
		body := mustReadRequest(tReader{r})
		category := strings.TrimPrefix(r.URL.Path, "/api/v1/export/metadata/")
		snapshot, perr := DecodeMetadataSnapshot(body, MetadataCategory(category))
		if perr != nil {
			writeTestError(w, perr.Code)
			return
		}
		f.mu.Lock()
		f.metadataBodies[category] = append(f.metadataBodies[category], append([]byte(nil), body...))
		f.metadataRevisions[category] = append(f.metadataRevisions[category], snapshot.Revision)
		f.mu.Unlock()
		select {
		case f.metadataSeen <- struct{}{}:
		default:
		}
		if current := f.keeperCurrentRevision.Load(); current > 0 {
			w.Header().Set("X-Keeper-Export-Current-Revision", strconv.FormatInt(current, 10))
		}
		if f.conflictingMetadataOnce.CompareAndSwap(true, false) {
			writeTestError(w, "conflicting_revision")
			return
		}
		if from := f.staleRevisionFrom.Load(); from > 0 && snapshot.Revision >= from {
			// Reset so the next attempt (after a one-shot jump) succeeds.
			f.staleRevisionFrom.Store(0)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"protocolVersion": ProtocolVersion, "error": map[string]any{"code": "stale_revision", "message": "revision is stale", "retryable": false}})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","category":"%s","revision":%d,"applied":true,"itemCount":%d,"serverTime":"2026-08-03T12:36:01.000Z"}`, category, snapshot.Revision, metadataItemCount(snapshot, MetadataCategory(category)))
	default:
		http.NotFound(w, r)
	}
}

type tReader struct{ r *http.Request }

func mustReadRequest(value tReader) []byte { body, _ := io.ReadAll(value.r.Body); return body }

func writeTestError(w http.ResponseWriter, code string) {
	spec := protocolError(code)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(spec.HTTPStatus)
	_ = json.NewEncoder(w).Encode(map[string]any{"protocolVersion": ProtocolVersion, "error": map[string]any{"code": spec.Code, "message": spec.Message, "retryable": spec.Retryable}})
}

func testExporterConfig(t *testing.T, server *httptest.Server) config.UsageExportConfig {
	t.Helper()
	dir := t.TempDir()
	certificate := server.Certificate()
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled, cfg.Mode = true, config.UsageExportModePush
	cfg.Keeper.URL, cfg.Keeper.TokenEnv, cfg.Keeper.CAFile = server.URL, "KEEPER_EXPORT_TEST_TOKEN", &caPath
	cfg.Outbox.MaxBytes = 16 << 20
	cfg.Delivery.FlushIntervalMs = 100
	cfg.Delivery.InitialBackoffMs = 100
	cfg.Delivery.MaxBackoffMs = 100
	cfg.Metadata.IntervalMs = 60000
	return cfg
}

func TestExporterDisconnectAfterCommitRetriesExactBatchAndCompacts(t *testing.T) {
	fake := newFakeKeeper()
	fake.disconnectOnce.Store(true)
	server := httptest.NewUnstartedServer(fake)
	server.EnableHTTP2 = false
	server.StartTLS()
	defer server.Close()
	cfg := testExporterConfig(t, server)
	t.Setenv(cfg.Keeper.TokenEnv, "token-one")
	var runtime Runtime
	runtime.SetSnapshotSource(func() SnapshotInput { return SnapshotInput{} })
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	awaitSignal(t, fake.identitySeen)
	awaitSignal(t, fake.metadataSeen)

	ctx := internallogging.WithRequestID(context.Background(), "duplicate-id")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, 200)
	record := coreusage.Record{Provider: "openai", ExecutorType: "CodexExecutor", Model: "gpt-5.6", APIKey: "raw-secret-key", AuthIndex: "auth-a", AuthType: "apikey", RequestedAt: time.Date(2026, 8, 3, 12, 34, 56, 789000000, time.UTC), Detail: coreusage.Detail{InputTokens: 1, TotalTokens: 1}}
	acked := make(chan struct{}, 1)
	runtime.mu.RLock()
	outbox := runtime.outbox
	runtime.mu.RUnlock()
	setBeforeAckCommitTestHook(outbox, func(context.Context) error { acked <- struct{}{}; return nil })
	runtime.HandleUsage(ctx, record)
	runtime.HandleUsage(ctx, record)
	awaitSignal(t, fake.usageSeen)
	awaitSignal(t, fake.usageSeen)
	awaitSignal(t, acked)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.usageBodies) < 2 {
		t.Fatalf("usage attempts = %d, want retry", len(fake.usageBodies))
	}
	if string(fake.usageBodies[0]) != string(fake.usageBodies[1]) {
		t.Fatal("same-batch retry changed exact request bytes")
	}
	if len(fake.usageSequences) < 4 || fake.usageSequences[0] != 1 || fake.usageSequences[1] != 2 || fake.usageSequences[2] != 1 || fake.usageSequences[3] != 2 {
		t.Fatalf("sequences = %v", fake.usageSequences)
	}
	for _, body := range fake.usageBodies {
		for _, secret := range []string{"raw-secret-key", "fail body", "Authorization"} {
			if strings.Contains(string(body), secret) {
				t.Fatalf("export body leaked %q", secret)
			}
		}
	}
}

func TestExporterDeliversUsageWithDirectKeeperToken(t *testing.T) {
	fake := newFakeKeeper()
	server := httptest.NewTLSServer(fake)
	defer server.Close()
	cfg := testExporterConfig(t, server)
	cfg.Keeper.Token = "token-one"
	cfg.Keeper.TokenEnv = ""

	var runtime Runtime
	runtime.SetSnapshotSource(func() SnapshotInput { return SnapshotInput{} })
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	awaitSignal(t, fake.identitySeen)
	awaitSignal(t, fake.metadataSeen)

	ctx := internallogging.WithRequestID(context.Background(), "req-direct-token")
	runtime.HandleUsage(ctx, coreusage.Record{
		Provider: "openai", Model: "gpt", AuthIndex: "a", Detail: coreusage.Detail{TotalTokens: 1},
	})
	awaitSignal(t, fake.usageSeen)
}

func TestExporterInvalidAckDoesNotCompactAndHotReloadRotatesToken(t *testing.T) {
	fake := newFakeKeeper()
	fake.invalidAck.Store(true)
	server := httptest.NewTLSServer(fake)
	defer server.Close()
	cfg := testExporterConfig(t, server)
	t.Setenv(cfg.Keeper.TokenEnv, "token-one")
	var runtime Runtime
	runtime.SetSnapshotSource(func() SnapshotInput { return SnapshotInput{} })
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, fake.identitySeen)
	awaitSignal(t, fake.metadataSeen)
	ctx := internallogging.WithRequestID(context.Background(), "req-invalid-ack")
	runtime.HandleUsage(ctx, coreusage.Record{Provider: "openai", Model: "gpt", AuthIndex: "a", Detail: coreusage.Detail{TotalTokens: 1}})
	awaitSignal(t, fake.usageSeen)
	status, err := runtime.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BacklogEvents != 1 {
		t.Fatalf("backlog = %d, want 1 after invalid ACK", status.BacklogEvents)
	}

	fake.invalidAck.Store(false)
	fake.expectedToken.Store("token-two")
	t.Setenv(cfg.Keeper.TokenEnv, "token-two")
	if err = runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	acked := make(chan struct{}, 1)
	runtime.mu.RLock()
	outbox := runtime.outbox
	runtime.mu.RUnlock()
	setBeforeAckCommitTestHook(outbox, func(context.Context) error { acked <- struct{}{}; return nil })
	awaitSignal(t, fake.identitySeen)
	awaitSignal(t, fake.metadataSeen)
	awaitSignal(t, acked)
}

func TestExporterRecoversConflictingMetadataRevision(t *testing.T) {
	fake := newFakeKeeper()
	fake.conflictingMetadataOnce.Store(true)
	server := httptest.NewTLSServer(fake)
	defer server.Close()
	cfg := testExporterConfig(t, server)
	t.Setenv(cfg.Keeper.TokenEnv, "token-one")
	var runtime Runtime
	runtime.SetSnapshotSource(func() SnapshotInput { return SnapshotInput{} })
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	awaitSignal(t, fake.identitySeen)
	for range 4 {
		awaitSignal(t, fake.metadataSeen)
	}
	fake.mu.Lock()
	revisions := append([]int64(nil), fake.metadataRevisions[string(CategoryAuthFiles)]...)
	fake.mu.Unlock()
	if len(revisions) != 2 || revisions[0] != 1 || revisions[1] != 2 {
		t.Fatalf("auth_files revisions=%v, want [1 2]", revisions)
	}
}

func TestMetadataProjectionPersistsIndependentRevisionsAndRedactsSecrets(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	input := SnapshotInput{Config: config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"client-secret-key"}}}, Auths: []*coreauth.Auth{{ID: "provider-a", Index: "provider-index", Provider: "openai-compatibility", Label: "OpenRouter", Prefix: "team", Attributes: map[string]string{"api_key": "provider-secret-key", "base_url": "https://openrouter.ai/api/v1", "note": "safe note"}}, {ID: "oauth-a", Index: "oauth-index", Provider: "codex", FileName: "/private/auth/codex.json", Label: "user@example.com", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "oauth-secret", "type": "codex"}}}}
	for _, category := range []MetadataCategory{CategoryAuthFiles, CategoryAPIKeys, CategoryProviderIdentities} {
		body, _, err := ProjectMetadata(input, secret, category, 1, time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"client-secret-key", "provider-secret-key", "oauth-secret", "/private/auth"} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("%s leaked %q", category, forbidden)
			}
		}
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out awaiting deterministic fake Keeper signal")
	}
}

var _ = x509.NewCertPool

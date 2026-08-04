package keeperexport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// newProbeServer starts a local HTTP server that answers every keeper request
// with a fast, stable 503 (retryable) response. It exists so the probe worker
// has a reachable endpoint that fails quickly instead of a black-holed address
// that would stall the retry loop. The handler closes the connection after
// each response so the worker never hangs on persistent-conn reads.
func newProbeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"service_unavailable"}}`))
	}))
	server.Config.SetKeepAlivesEnabled(false)
	t.Cleanup(server.Close)
	return server
}

// newBoundProbeRuntime builds an enabled runtime whose outbox is bound to an
// instance (fingerprint secret persisted), without needing a live Keeper.
func newBoundProbeRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	var runtime Runtime
	path := filepath.Join(t.TempDir(), "outbox.db")
	cfg := config.DefaultUsageExportConfig(filepath.Dir(path))
	cfg.Enabled = true
	cfg.Mode = config.UsageExportModePush
	cfg.Outbox.Path = path
	cfg.Outbox.MaxBytes = 16 << 20
	server := newProbeServer(t)
	cfg.Keeper.URL = server.URL // reachable; 503 keeps the worker busy but fast
	cfg.Keeper.TokenEnv = "TASK10_PROBE_TOKEN"
	t.Setenv("TASK10_PROBE_TOKEN", "probe-token-value")
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	ob, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatalf("OpenOutbox() error = %v", err)
	}
	if _, err := ob.BindInstance(context.Background(), "0198aa10-4d88-7a20-8f4e-8c8de4a9cb11"); err != nil {
		t.Fatalf("BindInstance() error = %v", err)
	}
	_ = ob.Close()
	return &runtime, path
}

// contextWithRequestID is a test helper shared by the observability tests.
func contextWithRequestID(t *testing.T, requestID string) context.Context {
	t.Helper()
	return internallogging.WithRequestID(context.Background(), requestID)
}

// configForAppendFailureProbe creates a config with a tiny outbox so that
// append failures can be deterministically triggered via test hooks.
func configForAppendFailureProbe(t *testing.T, dir, path string) config.UsageExportConfig {
	t.Helper()
	cfg := config.DefaultUsageExportConfig(filepath.Dir(path))
	cfg.Enabled = true
	cfg.Mode = config.UsageExportModePush
	cfg.Outbox.Path = path
	cfg.Outbox.MaxBytes = 16 << 20
	server := newProbeServer(t)
	cfg.Keeper.URL = server.URL
	cfg.Keeper.TokenEnv = "TASK10_OBS_TOKEN"
	t.Setenv("TASK10_OBS_TOKEN", "obs-token-value")
	return cfg
}

func probeRecord() coreusage.Record {
	return coreusage.Record{
		Provider:     "openai-compatible-task10-upstream",
		ExecutorType: "OpenAICompatExecutor",
		Model:        "task10-model",
		Alias:        "task10-model",
		APIKey:       "sk-task10-shared-client-key-0123456789abcdef",
		AuthType:     "apikey",
		AuthIndex:    "c0c4ae9e16fe3d5d",
		Source:       "sk-task10-shared-provider-key-0123456789abcdef",
		RequestedAt:  time.Now().UTC(),
		Detail:       coreusage.Detail{InputTokens: 11, OutputTokens: 7, TotalTokens: 18},
		Generate:     coreusage.GenerateFlag(true),
	}
}

// Control: a live (non-cancelled) request context appends successfully,
// proving the bound-runtime probe setup is correct and ctx cancellation is the
// sole cause of the drop in the cancelled-context case.
func TestTask10HandleUsageAppendsWithLiveRequestContext(t *testing.T) {
	runtime, path := newBoundProbeRuntime(t)

	ctx := internallogging.WithRequestID(context.Background(), "probe-live-request")
	runtime.HandleUsage(ctx, probeRecord())

	outbox, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	status, err := outbox.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BacklogEvents != 1 {
		t.Fatalf("BacklogEvents = %d, want 1 (live ctx should append)", status.BacklogEvents)
	}
}

// RED: reproduces the real proxy path where the usage dispatcher invokes
// HandleUsage with the request-scoped context AFTER the HTTP handler has
// returned and net/http has cancelled it. The durable outbox append must not
// depend on request-context lifetime.
func TestTask10HandleUsageSurvivesCancelledRequestContext(t *testing.T) {
	runtime, path := newBoundProbeRuntime(t)

	ctx, cancel := context.WithCancel(context.Background())
	ctx = internallogging.WithRequestID(ctx, "probe-cancelled-request")
	cancel() // simulate handler completion -> request context cancelled

	runtime.HandleUsage(ctx, probeRecord())

	outbox, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	status, err := outbox.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.BacklogEvents != 1 {
		t.Fatalf("BacklogEvents = %d, want 1 (usage event dropped when request ctx cancelled)", status.BacklogEvents)
	}
}

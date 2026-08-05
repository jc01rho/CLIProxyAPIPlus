package management

import (
	"bytes"
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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"
	log "github.com/sirupsen/logrus"
)

const managementTestInstanceID = "0198aa10-4d88-7a20-8f4e-8c8de4a9cb11"

func TestUsageExportSettingsRedactionPutPersistenceAndHotApply(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	initial := "# preserve me\nusage-statistics-enabled: true\nunknown-root: retained\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.UsageStatisticsEnabled = true
	cfg.UsageExport.Keeper.TokenEnv = "KEEPER_TEST_SECRET"
	t.Setenv("KEEPER_TEST_SECRET", "super-secret-token")
	h := NewHandler(cfg, configPath, nil)
	var applied *config.Config
	h.SetOnConfigApplied(func(candidate *config.Config) { applied = candidate.CloneForRuntime() })
	r := setupTestRouter(h)
	r.GET("/settings", h.GetUsageExportSettings)
	r.PUT("/settings", h.PutUsageExportSettings)

	get := httptest.NewRequest(http.MethodGet, "/settings", nil)
	getRR := httptest.NewRecorder()
	r.ServeHTTP(getRR, get)
	if getRR.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", getRR.Code, getRR.Body.String())
	}
	if strings.Contains(getRR.Body.String(), "super-secret-token") || !strings.Contains(getRR.Body.String(), `"tokenConfigured":true`) {
		t.Fatalf("GET redaction/configuration mismatch: %s", getRR.Body.String())
	}
	if got := getRR.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}

	outboxPath := filepath.Join(dir, "new-outbox.db")
	body := validUsageExportSettingsBody("https://keeper.example.com/base", "KEEPER_TEST_SECRET", outboxPath, nil)
	put := httptest.NewRequest(http.MethodPut, "/settings", bytes.NewReader(body))
	put.Header.Set("Content-Type", "application/json")
	putRR := httptest.NewRecorder()
	r.ServeHTTP(putRR, put)
	if putRR.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", putRR.Code, putRR.Body.String())
	}
	if applied == nil || applied.UsageExport.Keeper.URL != "https://keeper.example.com/base" {
		t.Fatalf("hot apply missing: %#v", applied)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{"# preserve me", "unknown-root: retained", "url: https://keeper.example.com/base"} {
		if !strings.Contains(string(saved), retained) {
			t.Fatalf("saved config missing %q:\n%s", retained, saved)
		}
	}
	if strings.Contains(string(saved), "super-secret-token") || strings.Contains(putRR.Body.String(), "super-secret-token") {
		t.Fatal("token leaked to config or response")
	}
}

func TestUsageExportSettingsPreserveDirectToken(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("usage-statistics-enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.UsageStatisticsEnabled = true
	cfg.UsageExport.Keeper.Token = "direct-secret-token"
	h := NewHandler(cfg, configPath, nil)
	r := setupTestRouter(h)
	r.GET("/settings", h.GetUsageExportSettings)
	r.PUT("/settings", h.PutUsageExportSettings)

	getRR := httptest.NewRecorder()
	r.ServeHTTP(getRR, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if getRR.Code != http.StatusOK || strings.Contains(getRR.Body.String(), "direct-secret-token") || !strings.Contains(getRR.Body.String(), `"tokenConfigured":true`) {
		t.Fatalf("direct token GET status=%d body=%s", getRR.Code, getRR.Body.String())
	}

	body := validUsageExportSettingsBody("http://192.168.0.50:8080", "", filepath.Join(dir, "outbox.db"), nil)
	put := httptest.NewRequest(http.MethodPut, "/settings", bytes.NewReader(body))
	put.Header.Set("Content-Type", "application/json")
	putRR := httptest.NewRecorder()
	r.ServeHTTP(putRR, put)
	if putRR.Code != http.StatusOK {
		t.Fatalf("direct token PUT status=%d body=%s", putRR.Code, putRR.Body.String())
	}
	if got := h.cfg.UsageExport.Keeper.Token; got != "direct-secret-token" {
		t.Fatalf("direct token was not preserved: %q", got)
	}
	if strings.Contains(putRR.Body.String(), "direct-secret-token") {
		t.Fatalf("direct token leaked in PUT response: %s", putRR.Body.String())
	}
}

func TestUsageExportPutStrictHTTPAndStableErrors(t *testing.T) {
	dir := t.TempDir()
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
	r.PUT("/settings", h.PutUsageExportSettings)
	valid := validUsageExportSettingsBody("https://keeper.example.com", "KEEPER_TOKEN", filepath.Join(dir, "outbox.db"), nil)

	tests := []struct {
		name        string
		contentType string
		encoding    string
		body        []byte
		status      int
		code        string
	}{
		{"missing content type", "", "", valid, 400, "invalid_field"},
		{"content encoding", "application/json", "identity", valid, 400, "invalid_field"},
		{"unknown raw token", "application/json", "", bytes.Replace(valid, []byte(`"tokenEnv":"KEEPER_TOKEN"`), []byte(`"tokenEnv":"KEEPER_TOKEN","token":"secret"`), 1), 400, "unknown_field"},
		{"response only tokenConfigured", "application/json", "", bytes.Replace(valid, []byte(`"tokenEnv":"KEEPER_TOKEN"`), []byte(`"tokenEnv":"KEEPER_TOKEN","tokenConfigured":true`), 1), 400, "unknown_field"},
		{"duplicate key", "application/json", "", bytes.Replace(valid, []byte(`"enabled":true`), []byte(`"enabled":true,"enabled":true`), 1), 400, "invalid_json"},
		{"missing privacy object", "application/json", "", removeUsageExportJSONKey(t, valid, []string{"settings"}, "privacy"), 400, "invalid_field"},
		{"missing protocol version", "application/json", "", removeUsageExportJSONKey(t, valid, nil, "protocolVersion"), 400, "invalid_field"},
		{"wrong protocol version", "application/json", "", bytes.Replace(valid, []byte(keeperexport.ProtocolVersion), []byte("keeper-export/v2"), 1), 422, "unsupported_protocol_version"},
		{"invalid URL scheme", "application/json", "", bytes.Replace(valid, []byte("https://keeper.example.com"), []byte("ftp://keeper.example.com"), 1), 422, "invalid_settings"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/settings", bytes.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			if tc.encoding != "" {
				req.Header.Set("Content-Encoding", tc.encoding)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tc.status || !strings.Contains(rr.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestUsageExportConnectionTestRejectsCrossOriginRedirect(t *testing.T) {
	redirected := make(chan struct{}, 1)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/v1/export/identity", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "redirect-ca.pem")
	pemBytes := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: source.Certificate().Raw}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: target.Certificate().Raw})...)
	if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled, cfg.Mode = true, config.UsageExportModePush
	cfg.Keeper.URL, cfg.Keeper.TokenEnv, cfg.Keeper.CAFile = source.URL, "REDIRECT_TOKEN", &caPath
	cfg.Outbox.MaxBytes = 16 << 20
	t.Setenv(cfg.Keeper.TokenEnv, "secret")
	if _, perr := keeperexport.TestConnection(context.Background(), cfg); perr == nil || perr.Code != "keeper_invalid_response" {
		t.Fatalf("redirect error=%v", perr)
	}
	select {
	case <-redirected:
		t.Fatal("connection test followed cross-origin redirect")
	default:
	}
}

func TestUsageExportConnectionTestSanitizesRemoteMarkerAndRejectsInvalidMedia(t *testing.T) {
	const marker = "REMOTE_SECRET_MARKER"
	tests := []struct {
		name            string
		status          int
		contentType     string
		contentEncoding string
		body            string
		wantStatus      int
		wantCode        string
	}{
		{
			name:        "remote envelope message",
			status:      http.StatusServiceUnavailable,
			contentType: "application/json; charset=utf-8",
			body:        `{"protocolVersion":"keeper-export/v1","error":{"code":"service_unavailable","message":"` + marker + ` remote body","retryable":true}}`,
			wantStatus:  http.StatusServiceUnavailable,
			wantCode:    "service_unavailable",
		},
		{
			name:        "text plain identity",
			status:      http.StatusOK,
			contentType: "text/plain; charset=utf-8",
			body:        validIdentityBody(),
			wantStatus:  http.StatusBadGateway,
			wantCode:    "keeper_invalid_response",
		},
		{
			name:            "encoded identity",
			status:          http.StatusOK,
			contentType:     "application/json; charset=utf-8",
			contentEncoding: "identity",
			body:            validIdentityBody(),
			wantStatus:      http.StatusBadGateway,
			wantCode:        "keeper_invalid_response",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			keeper := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				if tc.contentEncoding != "" {
					w.Header().Set("Content-Encoding", tc.contentEncoding)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer keeper.Close()
			dir := t.TempDir()
			caPath := filepath.Join(dir, "keeper-ca.pem")
			if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: keeper.Certificate().Raw}), 0o600); err != nil {
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
			t.Setenv("MARKER_TEST_TOKEN", "keeper-token")
			outboxPath := filepath.Join(dir, "must-not-open.db")
			requestBody := validUsageExportTestBody(keeper.URL, "MARKER_TEST_TOKEN", outboxPath, &caPath)
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			logger := log.StandardLogger()
			previousOutput := logger.Out
			log.SetOutput(&logs)
			defer log.SetOutput(previousOutput)

			req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(requestBody))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus || !strings.Contains(rr.Body.String(), `"code":"`+tc.wantCode+`"`) {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			statusRR := httptest.NewRecorder()
			r.ServeHTTP(statusRR, httptest.NewRequest(http.MethodGet, "/status", nil))
			for surface, value := range map[string]string{"test response": rr.Body.String(), "status": statusRR.Body.String(), "logs": logs.String()} {
				if strings.Contains(value, marker) {
					t.Fatalf("%s leaked remote marker: %s", surface, value)
				}
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
		})
	}
}

func TestUsageExportConnectionTestSuccessFailureAndNoMutation(t *testing.T) {
	var requests int
	keeper := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/base/api/v1/export/identity" || r.Header.Get("Authorization") != "Bearer keeper-token" {
			http.Error(w, "raw remote body must not escape", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","instance":{"instanceId":"%s","displayName":"test instance"},"credential":{"credentialId":"0198aa10-4d88-7a20-8f4e-8c8de4a9cb12","scopes":["usage:push","identity:test"]},"serverTime":"2026-08-03T12:35:00.000Z"}`, managementTestInstanceID)
	}))
	defer keeper.Close()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
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
	t.Setenv("KEEPER_TOKEN", "keeper-token")
	body := validUsageExportTestBody(keeper.URL+"/base", "KEEPER_TOKEN", filepath.Join(dir, "never-open.db"), &caPath)
	before, _ := os.ReadFile(configPath)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), managementTestInstanceID) {
		t.Fatalf("success status=%d body=%s", rr.Code, rr.Body.String())
	}
	if requests != 1 {
		t.Fatalf("identity requests=%d", requests)
	}
	if _, err := os.Stat(filepath.Join(dir, "never-open.db")); !os.IsNotExist(err) {
		t.Fatalf("connection test mutated outbox: %v", err)
	}
	after, _ := os.ReadFile(configPath)
	if !bytes.Equal(before, after) {
		t.Fatal("connection test mutated config")
	}

	t.Setenv("KEEPER_TOKEN", "")
	req = httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"code":"token_env_unset"`) {
		t.Fatalf("unset token status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "raw remote body") || strings.Contains(rr.Body.String(), keeper.URL) {
		t.Fatal("failure leaked remote body or URL")
	}
}

type fakeUsageExportRuntime struct{ response keeperexport.StatusResponse }

func (f *fakeUsageExportRuntime) ManagementStatus(context.Context) (keeperexport.StatusResponse, error) {
	return f.response, nil
}

// TestUsageExportStatusAcceptsAppendFailureShape proves the F1 fix: when the
// runtime observes a local outbox-append failure, the status endpoint must
// return HTTP 200 with a sanitized status (state=blocked/degraded) rather than
// HTTP 500. Before the fix, the append path classified local failures with
// retryable storage_error/service_unavailable codes while ManagementStatus
// produced no nextRetryAt, so the strict status decoder rejected the shape
// and the handler returned 500 internal_error.
func TestUsageExportStatusAcceptsAppendFailureShape(t *testing.T) {
	stream := "0198aa11-1055-7f12-8a00-e843d1e17522"
	next, ack := int64(1), int64(0)
	h := NewHandler(&config.Config{}, "", nil)
	h.SetUsageExportRuntime(&fakeUsageExportRuntime{response: keeperexport.StatusResponse{
		State:               keeperexport.StateBlocked,
		Enabled:             true,
		StreamID:            &stream,
		NextSequence:        &next,
		AcknowledgedThrough: &ack,
		MetadataRevisions:   map[string]int64{"auth_files": 0, "api_keys": 0, "provider_identities": 0},
		LastError:           &keeperexport.StatusError{Code: "invalid_field", Message: "request contains an invalid field", Retryable: false, At: "2026-08-04T06:00:00.000Z"},
	}})
	r := setupTestRouter(h)
	r.GET("/status", h.GetUsageExportStatus)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("status body parse: %v", err)
	}
	if decoded["state"] != "blocked" {
		t.Fatalf("expected state=blocked; got %v", decoded["state"])
	}
	if decoded["protocolVersion"] != keeperexport.ProtocolVersion {
		t.Fatalf("missing protocolVersion: %v", decoded["protocolVersion"])
	}
	lastError, ok := decoded["lastError"].(map[string]any)
	if !ok || lastError["code"] != "invalid_field" {
		t.Fatalf("expected lastError.code=invalid_field; got %v", decoded["lastError"])
	}
	if lastError["retryable"] != false {
		t.Fatalf("expected lastError.retryable=false; got %v", lastError["retryable"])
	}
}

func TestUsageExportFailedStorageApplyKeepsRuntimeAndExposesError(t *testing.T) {
	keeper := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.URL.Path == "/api/v1/export/identity" {
			fmt.Fprintf(w, `{"protocolVersion":"keeper-export/v1","instance":{"instanceId":"%s","displayName":"working instance"},"credential":{"credentialId":"0198aa10-4d88-7a20-8f4e-8c8de4a9cb12","scopes":["identity:test"]},"serverTime":"2026-08-03T12:35:00.000Z"}`, managementTestInstanceID)
			return
		}
		http.NotFound(w, r)
	}))
	defer keeper.Close()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "apply-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: keeper.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultUsageExportConfig(dir)
	cfg.Enabled, cfg.Mode = true, config.UsageExportModePush
	cfg.Keeper.URL, cfg.Keeper.TokenEnv, cfg.Keeper.CAFile = keeper.URL, "APPLY_TOKEN", &caPath
	cfg.Outbox.MaxBytes, cfg.Metadata.Enabled = 16<<20, false
	t.Setenv(cfg.Keeper.TokenEnv, "token")
	var runtime keeperexport.Runtime
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	bad := cfg
	bad.Outbox.Path = dir
	if err := runtime.Apply(context.Background(), bad); err == nil {
		t.Fatal("bad storage Apply succeeded")
	}
	status, err := runtime.ManagementStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.StreamID == nil || status.LastError == nil || status.LastError.Code != "storage_error" {
		t.Fatalf("failed apply status=%#v", status)
	}
}

func TestUsageExportStatusRejectsInvalidRuntimeShape(t *testing.T) {
	next, ack := int64(1), int64(0)
	stream := "0198aa11-1055-7f12-8a00-e843d1e17522"
	h := NewHandler(&config.Config{}, "", nil)
	h.SetUsageExportRuntime(&fakeUsageExportRuntime{response: keeperexport.StatusResponse{
		State:               keeperexport.StateStarting,
		Enabled:             true,
		StreamID:            &stream,
		NextSequence:        &next,
		AcknowledgedThrough: &ack,
		MetadataRevisions:   map[string]int64{"auth_files": 0, "api_keys": 0, "provider_identities": 0, "unexpected": 1},
	}})
	r := setupTestRouter(h)
	r.GET("/status", h.GetUsageExportStatus)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestUsageExportStatusExactRedactedShape(t *testing.T) {
	next, ack, expected := int64(13), int64(12), int64(13)
	stream := "0198aa11-1055-7f12-8a00-e843d1e17522"
	oldest := "2026-08-03T12:38:00.000Z"
	attempted := "2026-08-03T12:39:59.000Z"
	succeeded := "2026-08-03T12:39:59.100Z"
	retry := "2026-08-03T12:40:05.000Z"
	h := NewHandler(&config.Config{}, "", nil)
	h.SetUsageExportRuntime(&fakeUsageExportRuntime{response: keeperexport.StatusResponse{
		State: keeperexport.StateRetrying, Enabled: true, TokenConfigured: true,
		Instance: &keeperexport.InstanceRef{InstanceID: managementTestInstanceID, DisplayName: "test instance"},
		StreamID: &stream, NextSequence: &next, AcknowledgedThrough: &ack, NextExpectedSequence: &expected,
		BacklogEvents: 3, BacklogBytes: 4096, OldestBacklogAt: &oldest,
		LastAttemptAt: &attempted, LastSuccessAt: &succeeded, NextRetryAt: &retry,
		MetadataRevisions: map[string]int64{"auth_files": 7, "api_keys": 3, "provider_identities": 9},
		LastError:         &keeperexport.StatusError{Code: "keeper_timeout", Message: "keeper request timed out", Retryable: true, At: "2026-08-03T12:39:59.000Z"},
	}})
	r := setupTestRouter(h)
	r.GET("/status", h.GetUsageExportStatus)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, forbidden := range []string{"tokenEnv", "Authorization", "outbox", "path", "payload"} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, rr.Body.String())
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["state"] != "retrying" || decoded["protocolVersion"] != keeperexport.ProtocolVersion {
		t.Fatalf("unexpected status: %s", rr.Body.String())
	}
}

func removeUsageExportJSONKey(t *testing.T, data []byte, path []string, key string) []byte {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var remove func(map[string]json.RawMessage, []string)
	remove = func(current map[string]json.RawMessage, remaining []string) {
		if len(remaining) == 0 {
			delete(current, key)
			return
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(current[remaining[0]], &nested); err != nil {
			t.Fatal(err)
		}
		remove(nested, remaining[1:])
		rebuilt, err := json.Marshal(nested)
		if err != nil {
			t.Fatal(err)
		}
		current[remaining[0]] = rebuilt
	}
	remove(root, path)
	result, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func validIdentityBody() string {
	return fmt.Sprintf(`{"protocolVersion":"keeper-export/v1","instance":{"instanceId":"%s","displayName":"test instance"},"credential":{"credentialId":"0198aa10-4d88-7a20-8f4e-8c8de4a9cb12","scopes":["usage:push","identity:test"]},"serverTime":"2026-08-03T12:35:00.000Z"}`, managementTestInstanceID)
}

func validUsageExportSettingsBody(url, tokenEnv, outbox string, caFile *string) []byte {
	ca := "null"
	if caFile != nil {
		encoded, _ := json.Marshal(*caFile)
		ca = string(encoded)
	}
	return []byte(fmt.Sprintf(`{"protocolVersion":"keeper-export/v1","settings":{"enabled":true,"mode":"push","keeper":{"url":%q,"tokenEnv":%q,"caFile":%s,"clientCertFile":null,"clientKeyFile":null},"outbox":{"path":%q,"maxBytes":16777216},"delivery":{"maxBatchEvents":100,"maxBatchBytes":65536,"flushIntervalMs":100,"requestTimeoutMs":1000,"initialBackoffMs":100,"maxBackoffMs":100},"metadata":{"enabled":true,"intervalMs":60000,"categories":["auth_files","api_keys","provider_identities"]},"privacy":{"includeClientIp":false,"includeForwardedFor":false,"includeUserAgent":false}}}`, url, tokenEnv, ca, outbox))
}

func validUsageExportTestBody(url, tokenEnv, outbox string, caFile *string) []byte {
	settings := validUsageExportSettingsBody(url, tokenEnv, outbox, caFile)
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(settings, &envelope)
	return []byte(fmt.Sprintf(`{"protocolVersion":"keeper-export/v1","settings":%s}`, envelope["settings"]))
}

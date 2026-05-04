package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func TestHealthz(t *testing.T) {
	server := newTestServer(t)

	t.Run("GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
		}
		if resp.Status != "ok" {
			t.Fatalf("unexpected response status: got %q want %q", resp.Status, "ok")
		}
	})

	t.Run("HEAD", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("expected empty body for HEAD request, got %q", rr.Body.String())
		}
	})
}

func TestScannerProbePathsAreBlockedBeforeRouting(t *testing.T) {
	server := newTestServer(t)

	testCases := []string{
		"/mailer/help/info.php",
		"/config/env/mailer_dsn.env",
		"/usr/local/app/.env",
		"/api/backup/database.sql",
		"/hidden/.env",
		"/live/.env.production",
		"/.env.backup.inactive",
		"/backup.env",
		"/api/new/wp-config.bak",
		"/s3/.aws/config.js",
		"/.aws/credentials.jpg",
		"/server/laravel/.env",
		"/config/json/default.bak",
		"/painel/laravel.log",
		"/wp-admin/.git/config",
	}

	for _, path := range testCases {
		path := path
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Fatalf("expected scanner probe to return 403, got %d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAuthMiddleware_BlocksAfterRepeatedInvalidAPIKeys(t *testing.T) {
	server := newTestServer(t)
	rootOKReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootOKReq.RemoteAddr = "203.0.113.20:1234"
	rootOK := httptest.NewRecorder()
	server.engine.ServeHTTP(rootOK, rootOKReq)
	if rootOK.Code != http.StatusOK {
		t.Fatalf("expected root endpoint to remain public before IP block, got %d body=%s", rootOK.Code, rootOK.Body.String())
	}

	performRequest := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.RemoteAddr = "203.0.113.20:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		return rr
	}

	for i := 0; i < 3; i++ {
		rr := performRequest("wrong-key")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d body=%s", i+1, rr.Code, rr.Body.String())
		}
	}

	blocked := performRequest("wrong-key")
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("expected 403 after threshold reached, got %d body=%s", blocked.Code, blocked.Body.String())
	}
	if !strings.Contains(blocked.Body.String(), "IP blocked") {
		t.Fatalf("expected blocked response body, got %s", blocked.Body.String())
	}

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootReq.RemoteAddr = "203.0.113.20:1234"
	rootBlocked := httptest.NewRecorder()
	server.engine.ServeHTTP(rootBlocked, rootReq)
	if rootBlocked.Code != http.StatusForbidden {
		t.Fatalf("expected root endpoint to return 403 for blocked IP, got %d body=%s", rootBlocked.Code, rootBlocked.Body.String())
	}
	if !strings.Contains(rootBlocked.Body.String(), "IP blocked") {
		t.Fatalf("expected root blocked response body, got %s", rootBlocked.Body.String())
	}
}

func TestAuthMiddleware_SuccessClearsPendingFailures(t *testing.T) {
	server := newTestServer(t)

	performRequest := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.RemoteAddr = "203.0.113.21:1234"
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		return rr
	}

	for i := 0; i < 2; i++ {
		rr := performRequest("wrong-key")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d body=%s", i+1, rr.Code, rr.Body.String())
		}
	}

	valid := performRequest("test-key")
	if valid.Code != http.StatusOK {
		t.Fatalf("expected successful auth to pass, got %d body=%s", valid.Code, valid.Body.String())
	}

	for i := 0; i < 2; i++ {
		rr := performRequest("wrong-key")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("post-reset attempt %d: expected 401, got %d body=%s", i+1, rr.Code, rr.Body.String())
		}
	}

	stillUnauthorized := performRequest("wrong-key")
	if stillUnauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected threshold to restart after success reset, got %d body=%s", stillUnauthorized.Code, stillUnauthorized.Body.String())
	}
}

func TestManagementUsageRequiresManagementAuthAndPopsArray(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	})

	server := newTestServer(t)

	redisqueue.Enqueue([]byte(`{"id":1}`))
	redisqueue.Enqueue([]byte(`{"id":2}`))

	missingKeyReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)
	missingKeyRR := httptest.NewRecorder()
	server.engine.ServeHTTP(missingKeyRR, missingKeyReq)
	if missingKeyRR.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want %d body=%s", missingKeyRR.Code, http.StatusUnauthorized, missingKeyRR.Body.String())
	}

	legacyReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage?count=2", nil)
	legacyReq.Header.Set("Authorization", "Bearer test-management-key")
	legacyRR := httptest.NewRecorder()
	server.engine.ServeHTTP(legacyRR, legacyReq)
	if legacyRR.Code != http.StatusNotFound {
		t.Fatalf("legacy usage status = %d, want %d body=%s", legacyRR.Code, http.StatusNotFound, legacyRR.Body.String())
	}

	authReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)
	authReq.Header.Set("Authorization", "Bearer test-management-key")
	authRR := httptest.NewRecorder()
	server.engine.ServeHTTP(authRR, authReq)
	if authRR.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d body=%s", authRR.Code, http.StatusOK, authRR.Body.String())
	}

	var payload []json.RawMessage
	if errUnmarshal := json.Unmarshal(authRR.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v body=%s", errUnmarshal, authRR.Body.String())
	}
	if len(payload) != 2 {
		t.Fatalf("response records = %d, want 2", len(payload))
	}
	for i, raw := range payload {
		var record struct {
			ID int `json:"id"`
		}
		if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
			t.Fatalf("unmarshal record %d: %v", i, errUnmarshal)
		}
		if record.ID != i+1 {
			t.Fatalf("record %d id = %d, want %d", i, record.ID, i+1)
		}
	}

	if remaining := redisqueue.PopOldest(1); len(remaining) != 0 {
		t.Fatalf("remaining queue = %q, want empty", remaining)
	}
}

func TestAmpProviderModelRoutes(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "openai root models",
			path:         "/api/provider/openai/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "groq root models",
			path:         "/api/provider/groq/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "openai models",
			path:         "/api/provider/openai/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "anthropic models",
			path:         "/api/provider/anthropic/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"data"`,
		},
		{
			name:         "google models v1",
			path:         "/api/provider/google/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
		{
			name:         "google models v1beta",
			path:         "/api/provider/google/v1beta/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantContains) {
				t.Fatalf("response body for %s missing %q: %s", tc.path, tc.wantContains, body)
			}
		})
	}
}

func TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")

	originalWD, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("failed to get current working directory: %v", errGetwd)
	}

	tmpDir := t.TempDir()
	if errChdir := os.Chdir(tmpDir); errChdir != nil {
		t.Fatalf("failed to switch working directory: %v", errChdir)
	}
	defer func() {
		if errChdirBack := os.Chdir(originalWD); errChdirBack != nil {
			t.Fatalf("failed to restore working directory: %v", errChdirBack)
		}
	}()

	// Force ResolveLogDirectory to fallback to auth-dir/logs by making ./logs not a writable directory.
	if errWriteFile := os.WriteFile(filepath.Join(tmpDir, "logs"), []byte("not-a-directory"), 0o644); errWriteFile != nil {
		t.Fatalf("failed to create blocking logs file: %v", errWriteFile)
	}

	configDir := filepath.Join(tmpDir, "config")
	if errMkdirConfig := os.MkdirAll(configDir, 0o755); errMkdirConfig != nil {
		t.Fatalf("failed to create config dir: %v", errMkdirConfig)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	authDir := filepath.Join(tmpDir, "auth")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog: false,
		},
		AuthDir:           authDir,
		ErrorLogsMaxFiles: 10,
	}

	logger := defaultRequestLoggerFactory(cfg, configPath)
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}

	errLog := fileLogger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"issue-1711",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("failed to write forced error request log: %v", errLog)
	}

	authLogsDir := filepath.Join(authDir, "logs")
	authEntries, errReadAuthDir := os.ReadDir(authLogsDir)
	if errReadAuthDir != nil {
		t.Fatalf("failed to read auth logs dir %s: %v", authLogsDir, errReadAuthDir)
	}
	foundErrorLogInAuthDir := false
	for _, entry := range authEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			foundErrorLogInAuthDir = true
			break
		}
	}
	if !foundErrorLogInAuthDir {
		t.Fatalf("expected forced error log in auth fallback dir %s, got entries: %+v", authLogsDir, authEntries)
	}

	configLogsDir := filepath.Join(configDir, "logs")
	configEntries, errReadConfigDir := os.ReadDir(configLogsDir)
	if errReadConfigDir != nil && !os.IsNotExist(errReadConfigDir) {
		t.Fatalf("failed to inspect config logs dir %s: %v", configLogsDir, errReadConfigDir)
	}
	for _, entry := range configEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in config dir %s", configLogsDir)
		}
	}
}

func TestNewServer_InjectsOAuthModelAliasIntoAuthManager(t *testing.T) {
	server, authManager, _ := newAliasWiringTestServer(t, map[string][]proxyconfig.OAuthModelAlias{
		"claude": {{Name: "claude-haiku-4-5-20251001", Alias: "haiku-cc", Fork: true}},
	})
	defer server.Stop(context.Background())

	assertManagerExecutesAlias(t, authManager, "haiku-cc", "claude-haiku-4-5-20251001")
}

func TestServerUpdateClients_RefreshesOAuthModelAliasIntoAuthManager(t *testing.T) {
	server, authManager, cfg := newAliasWiringTestServer(t, nil)
	defer server.Stop(context.Background())

	updated := *cfg
	updated.OAuthModelAlias = map[string][]proxyconfig.OAuthModelAlias{
		"claude": {{Name: "claude-haiku-4-5-20251001", Alias: "haiku-cc", Fork: true}},
	}
	server.UpdateClients(&updated)

	assertManagerExecutesAlias(t, authManager, "haiku-cc", "claude-haiku-4-5-20251001")
}

type aliasCaptureExecutor struct {
	id     string
	mu     sync.Mutex
	models []string
}

func (e *aliasCaptureExecutor) Identifier() string { return e.id }

func (e *aliasCaptureExecutor) Execute(_ context.Context, _ *auth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.models = append(e.models, req.Model)
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(req.Model), Headers: make(http.Header)}, nil
}

func (e *aliasCaptureExecutor) ExecuteStream(context.Context, *auth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &auth.Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *aliasCaptureExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *aliasCaptureExecutor) CountTokens(context.Context, *auth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &auth.Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *aliasCaptureExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, &auth.Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *aliasCaptureExecutor) Models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.models))
	copy(out, e.models)
	return out
}

func newAliasWiringTestServer(t *testing.T, aliases map[string][]proxyconfig.OAuthModelAlias) (*Server, *auth.Manager, *proxyconfig.Config) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
		OAuthModelAlias:        aliases,
	}
	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()
	server := NewServer(cfg, authManager, accessManager, filepath.Join(tmpDir, "config.yaml"))
	return server, authManager, cfg
}

func assertManagerExecutesAlias(t *testing.T, manager *auth.Manager, routeModel, wantModel string) {
	t.Helper()

	executor := &aliasCaptureExecutor{id: "claude"}
	manager.RegisterExecutor(executor)

	authID := fmt.Sprintf("claude-oauth-%s", strings.ReplaceAll(t.Name(), "/", "-"))
	authEntry := &auth.Auth{
		ID:       authID,
		Provider: "claude",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"email": "claude@example.com",
		},
	}
	registered, err := manager.Register(context.Background(), authEntry)
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(registered.ID, registered.Provider, []*registry.ModelInfo{{ID: wantModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(registered.ID)
	})
	manager.RefreshSchedulerEntry(registered.ID)

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if got := string(resp.Payload); got != wantModel {
		t.Fatalf("response payload = %q, want %q", got, wantModel)
	}
	models := executor.Models()
	if len(models) != 1 {
		t.Fatalf("executed models = %v, want single %q", models, wantModel)
	}
	if models[0] != wantModel {
		t.Fatalf("executed model = %q, want %q", models[0], wantModel)
	}
}

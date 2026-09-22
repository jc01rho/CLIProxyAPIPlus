package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type refreshRecordExecutor struct {
	provider   string
	refreshCnt atomic.Int32
}

func (e *refreshRecordExecutor) Identifier() string {
	return e.provider
}

func (e *refreshRecordExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.refreshCnt.Add(1)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-token"
	auth.Metadata["refresh_token"] = "refresh-token"
	auth.Metadata["expires_in"] = int64(3600)
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

func (e *refreshRecordExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshRecordExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *refreshRecordExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshRecordExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRefreshAuthFiles_AllAndSpecific(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	fileA := filepath.Join(authDir, "antigravity-1.json")
	fileB := filepath.Join(authDir, "antigravity-2.json")
	_ = os.WriteFile(fileA, []byte(`{"type":"antigravity","refresh_token":"ref-1","access_token":"old-1"}`), 0o600)
	_ = os.WriteFile(fileB, []byte(`{"type":"antigravity","refresh_token":"ref-2","access_token":"old-2"}`), 0o600)

	manager := coreauth.NewManager(nil, nil, nil)
	exec := &refreshRecordExecutor{provider: "antigravity"}
	manager.RegisterExecutor(exec)

	auth1 := &coreauth.Auth{
		ID:       "antigravity-1.json",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "antigravity", "refresh_token": "ref-1", "access_token": "old-1"},
	}
	auth2 := &coreauth.Auth{
		ID:          "antigravity-2.json",
		Provider:    "antigravity",
		Status:      coreauth.StatusError,
		Unavailable: true,
		LastError:   &coreauth.Error{Message: "unauthorized"},
		Metadata:    map[string]any{"type": "antigravity", "refresh_token": "ref-2", "access_token": "old-2"},
	}
	_, _ = manager.Register(context.Background(), auth1)
	_, _ = manager.Register(context.Background(), auth2)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	modelRefreshCalls := atomic.Int32{}
	h.SetModelRegistrationRefreshHook(func(context.Context, *coreauth.Auth) error {
		modelRefreshCalls.Add(1)
		return nil
	})

	engine := gin.New()
	engine.POST("/auth-files/refresh", h.RefreshAuthFiles)

	// 1. Refresh all
	req := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?all=true", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected ok=true, got %v", resp)
	}

	if cnt := exec.refreshCnt.Load(); cnt < 2 {
		t.Fatalf("expected at least 2 refreshes, got %d", cnt)
	}

	// 2. Auth2 was in StatusError, now should be active/recovering
	a2, exists := manager.GetByID("antigravity-2.json")
	if !exists || a2.Status == coreauth.StatusError {
		t.Fatalf("expected auth2 status to be recovered from error, got %+v", a2)
	}

	// 3. Refresh single file by name
	prevCnt := exec.refreshCnt.Load()
	reqSingle := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?name=antigravity-1.json", nil)
	wSingle := httptest.NewRecorder()
	engine.ServeHTTP(wSingle, reqSingle)

	if wSingle.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", wSingle.Code, wSingle.Body.String())
	}
	if newCnt := exec.refreshCnt.Load(); newCnt != prevCnt+1 {
		t.Fatalf("expected cnt to increment by 1, was %d now %d", prevCnt, newCnt)
	}
	if got := modelRefreshCalls.Load(); got < 1 {
		t.Fatalf("expected model registration refresh after token refresh, got %d calls", got)
	}

	// 4. Refresh nonexistent file
	reqMissing := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?name=nonexistent.json", nil)
	wMissing := httptest.NewRecorder()
	engine.ServeHTTP(wMissing, reqMissing)

	if wMissing.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", wMissing.Code, wMissing.Body.String())
	}

	// 5. Refresh via chunked JSON request body (ContentLength = -1)
	chunkedBody := strings.NewReader(`{"name":"antigravity-1.json"}`)
	reqChunked := httptest.NewRequest(http.MethodPost, "/auth-files/refresh", chunkedBody)
	reqChunked.Header.Set("Content-Type", "application/json")
	reqChunked.TransferEncoding = []string{"chunked"}
	reqChunked.ContentLength = -1
	wChunked := httptest.NewRecorder()
	engine.ServeHTTP(wChunked, reqChunked)

	if wChunked.Code != http.StatusOK {
		t.Fatalf("expected chunked request status 200, got %d: %s", wChunked.Code, wChunked.Body.String())
	}

	// 6. Malformed JSON request body returns 400
	reqBadJSON := httptest.NewRequest(http.MethodPost, "/auth-files/refresh", strings.NewReader(`{invalid`))
	reqBadJSON.Header.Set("Content-Type", "application/json")
	wBadJSON := httptest.NewRecorder()
	engine.ServeHTTP(wBadJSON, reqBadJSON)

	if wBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected malformed JSON to return 400, got %d", wBadJSON.Code)
	}
}

func TestRefreshAuthFiles_ExpiresNeverRefreshesModelsWithoutTokenRotation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	exec := &refreshRecordExecutor{provider: "claude"}
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{
		ID:       "claude-sajjon.json",
		FileName: "claude-sajjon.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token":  "static-token",
			"refresh_token": "must-not-be-used",
			"expires_never": true,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	modelRefreshCalls := 0
	h.SetModelRegistrationRefreshHook(func(_ context.Context, refreshedAuth *coreauth.Auth) error {
		modelRefreshCalls++
		if refreshedAuth.ID != auth.ID {
			t.Fatalf("refresh auth ID = %q, want %q", refreshedAuth.ID, auth.ID)
		}
		return nil
	})

	engine := gin.New()
	engine.POST("/auth-files/refresh", h.RefreshAuthFiles)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?name=claude-sajjon.json", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := exec.refreshCnt.Load(); got != 0 {
		t.Fatalf("token refresh calls = %d, want 0 for expires-never auth", got)
	}
	if modelRefreshCalls != 1 {
		t.Fatalf("model registration refresh calls = %d, want 1", modelRefreshCalls)
	}
}

package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestRequestDevinTokenReturnsAuthURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir}
	h := NewHandlerWithoutConfigFilePath(cfg, nil)

	router := gin.New()
	router.GET("/v0/management/devin-auth-url", h.RequestDevinToken)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/devin-auth-url", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.URL == "" || !strings.Contains(resp.URL, "https://app.devin.ai/auth/cli/continue") {
		t.Errorf("unexpected URL: %s", resp.URL)
	}
	if resp.State == "" {
		t.Errorf("state should not be empty")
	}
	if !strings.Contains(resp.URL, "state="+resp.State) {
		t.Errorf("auth URL missing state param: %s", resp.URL)
	}
	if !strings.Contains(resp.URL, "cli_pkce_marker=1") {
		t.Errorf("auth URL missing cli_pkce_marker: %s", resp.URL)
	}

	// Clean up session
	CompleteOAuthSession(resp.State)
}

func TestPostOAuthCallbackPersistsDevinCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	state := "test-devin-state-123"
	RegisterOAuthSession(state, "devin")
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.POST("/v0/management/oauth-callback", h.PostOAuthCallback)

	body := `{"provider":"devin","code":"wspkce$test-auth-code-456","state":"test-devin-state-123"}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	callbackPath := filepath.Join(authDir, ".oauth-devin-"+state+".oauth")
	data, errRead := os.ReadFile(callbackPath)
	if errRead != nil {
		t.Fatalf("expected callback file to be written: %v", errRead)
	}

	var payload oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode callback payload: %v", errUnmarshal)
	}
	if payload.State != state || payload.Code != "wspkce$test-auth-code-456" {
		t.Fatalf("unexpected callback payload: %+v", payload)
	}
}

func TestNormalizeOAuthProviderDevin(t *testing.T) {
	norm, err := NormalizeOAuthProvider("devin")
	if err != nil {
		t.Fatalf("NormalizeOAuthProvider(devin) failed: %v", err)
	}
	if norm != "devin" {
		t.Errorf("expected devin, got %s", norm)
	}

	normUpper, err := NormalizeOAuthProvider("DEVIN")
	if err != nil {
		t.Fatalf("NormalizeOAuthProvider(DEVIN) failed: %v", err)
	}
	if normUpper != "devin" {
		t.Errorf("expected devin, got %s", normUpper)
	}
}

func TestDevinOAuthLifecycleEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir}
	h := NewHandlerWithoutConfigFilePath(cfg, nil)

	router := gin.New()
	router.GET("/v0/management/devin-auth-url", h.RequestDevinToken)
	router.GET("/v0/management/get-auth-status", h.GetAuthStatus)
	router.POST("/v0/management/oauth-callback", h.PostOAuthCallback)

	// Step 1: UI requests start auth
	reqStart := httptest.NewRequest(http.MethodGet, "/v0/management/devin-auth-url", nil)
	wStart := httptest.NewRecorder()
	router.ServeHTTP(wStart, reqStart)

	if wStart.Code != http.StatusOK {
		t.Fatalf("start auth failed: %d %s", wStart.Code, wStart.Body.String())
	}

	var startResp struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	_ = json.Unmarshal(wStart.Body.Bytes(), &startResp)
	state := startResp.State

	// Step 2: UI polls status -> should be "wait"
	reqStatus := httptest.NewRequest(http.MethodGet, "/v0/management/get-auth-status?state="+state, nil)
	wStatus := httptest.NewRecorder()
	router.ServeHTTP(wStatus, reqStatus)

	var statusResp struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(wStatus.Body.Bytes(), &statusResp)
	if statusResp.Status != "wait" {
		t.Fatalf("expected status wait, got %s", statusResp.Status)
	}

	// Step 3: Simulate callback file with valid mock tokens by setting up a callback write
	// In the real flow, the background goroutine in RequestDevinToken calls ExchangeCode.
	// We verify that submitting callback persists the callback file correctly for that session.
	callbackBody := `{"provider":"devin","redirect_url":"http://localhost/oauth-callback?code=mock-code-xyz","state":"` + state + `"}`
	reqCb := httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(callbackBody))
	reqCb.Header.Set("Content-Type", "application/json")
	wCb := httptest.NewRecorder()
	router.ServeHTTP(wCb, reqCb)

	if wCb.Code != http.StatusOK {
		t.Fatalf("callback submission failed: %d %s", wCb.Code, wCb.Body.String())
	}

	// Verify session can be completed and status becomes "ok"
	CompleteOAuthSession(state)

	reqStatusFinal := httptest.NewRequest(http.MethodGet, "/v0/management/get-auth-status?state="+state, nil)
	wStatusFinal := httptest.NewRecorder()
	router.ServeHTTP(wStatusFinal, reqStatusFinal)

	_ = json.Unmarshal(wStatusFinal.Body.Bytes(), &statusResp)
	if statusResp.Status != "ok" {
		t.Fatalf("expected status ok after completion, got %s", statusResp.Status)
	}

	// Verify token record builder directly creates valid file structure in AuthDir
	mockTokens := &devinauth.TokenResponse{
		APIKey:          "live-api-key-test-abc",
		APIServerURL:    "https://server.codeium.com",
		DevinAPIURL:     "https://api.devin.ai",
		DevinWebappHost: "https://app.devin.ai",
		SessionToken:    "session-tok-123",
	}
	record := devinauth.BuildAuthRecord(mockTokens, "lifecycle")
	savedPath, errSave := h.saveTokenRecord(t.Context(), record)
	if errSave != nil {
		t.Fatalf("saveTokenRecord failed: %v", errSave)
	}

	if _, errStat := os.Stat(savedPath); errStat != nil {
		t.Fatalf("saved auth file does not exist at %s: %v", savedPath, errStat)
	}

	data, _ := os.ReadFile(savedPath)
	if !strings.Contains(string(data), "live-api-key-test-abc") {
		t.Errorf("saved file does not contain api_key: %s", string(data))
	}
	if !strings.Contains(string(data), "https://api.devin.ai") {
		t.Errorf("saved file does not contain devin_api_url: %s", string(data))
	}
}

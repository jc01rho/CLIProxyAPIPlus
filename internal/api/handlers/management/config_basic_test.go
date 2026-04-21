package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestDetailedAPIErrorBodyLogFormatGetDefaultsToFull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/detailed-api-error-body-log-format", nil)
	h.GetDetailedAPIErrorBodyLogFormat(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"detailed-api-error-body-log-format":"full"`) {
		t.Fatalf("expected default full format, got %s", rec.Body.String())
	}
}

func TestDetailedAPIErrorBodyLogFormatPutAcceptsSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	configPath := dir + "/config.yaml"
	if err := os.WriteFile(configPath, []byte("auth-dir: \""+dir+"\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogFormat: "full"}, AuthDir: dir}, nil)
	h.configFilePath = configPath
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/detailed-api-error-body-log-format", strings.NewReader(`{"value":"summary"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PutDetailedAPIErrorBodyLogFormat(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if h.cfg.DetailedAPIErrorBodyLogFormat != "summary" {
		t.Fatalf("expected format to be summary, got %q", h.cfg.DetailedAPIErrorBodyLogFormat)
	}
}

func TestDetailedAPIErrorBodyLogFormatPutRejectsInvalidValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/detailed-api-error-body-log-format", strings.NewReader(`{"value":"verbose"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PutDetailedAPIErrorBodyLogFormat(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

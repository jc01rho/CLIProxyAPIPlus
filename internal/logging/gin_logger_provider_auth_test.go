package logging

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

func TestGinLogrusLoggerIncludesProviderAuthLabelWithAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.InfoLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger(&config.Config{}))
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set(ginProviderAuthKey, map[string]string{
			"provider":   "codex",
			"auth_id":    "auth-file-1.json",
			"auth_label": "someone@example.com",
		})
		c.Set(ginFallbackInfoKey, map[string]string{
			"requested_model": "higher-coding",
			"actual_model":    "deepseek-ai/deepseek-v4-pro",
		})
		c.Set(ginAPIRequestSummaryKey, map[string]string{
			"url":   "https://example.com/v1/chat/completions",
			"model": "deepseek-ai/deepseek-v4-pro",
		})
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"higher-coding"}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	want := "higher-coding → deepseek-ai/deepseek-v4-pro | codex:someone@example.com"
	if !bytes.Contains([]byte(logOutput), []byte(want)) {
		t.Fatalf("expected provider auth label with alias in log, got: %s", logOutput)
	}
}

func TestGinLogrusLoggerFallsBackToProviderAuthID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.InfoLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger(&config.Config{}))
	engine.POST("/v1/responses", func(c *gin.Context) {
		c.Set(ginProviderAuthKey, map[string]string{
			"provider": "codex",
			"auth_id":  "auth-file-1.json",
		})
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5.6-sol"}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	if !bytes.Contains([]byte(logOutput), []byte("gpt-5.6-sol | codex:auth-file-1.json")) {
		t.Fatalf("expected provider auth ID fallback in log, got: %s", logOutput)
	}
}

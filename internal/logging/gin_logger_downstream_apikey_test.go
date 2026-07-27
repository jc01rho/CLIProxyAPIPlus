package logging

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// extractDownstreamAPIKey only logs redacted previews: scheme + 4-char prefix
// + total length. The raw secret must never appear in the log line.
func TestExtractDownstreamAPIKey_NeverLeaksRawSecret(t *testing.T) {
	const secret = "sk-prod-THISISTHEACTUALSECRETDONOTLEAK"
	cases := []struct {
		name    string
		headers http.Header
	}{
		{"bearer", http.Header{"Authorization": {"Bearer " + secret}}},
		{"x-api-key", http.Header{"X-Api-Key": {secret}}},
		{"api-key", http.Header{"Api-Key": {secret}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := extractDownstreamAPIKey(tc.headers)
			if out == "" {
				t.Fatalf("expected redacted preview, got empty")
			}
			if strings.Contains(out, secret) {
				t.Fatalf("raw secret leaked into redacted output: %s", out)
			}
			if !strings.Contains(out, secret[:4]) {
				t.Fatalf("expected 4-char prefix in preview, got: %s", out)
			}
		})
	}
}

func TestExtractDownstreamAPIKey_EmptyOrMalformed(t *testing.T) {
	if got := extractDownstreamAPIKey(nil); got != "" {
		t.Fatalf("expected empty for nil headers, got %q", got)
	}
	if got := extractDownstreamAPIKey(http.Header{}); got != "" {
		t.Fatalf("expected empty for empty headers, got %q", got)
	}
	if got := extractDownstreamAPIKey(http.Header{"Authorization": {"Basic xyz"}}); got != "" {
		t.Fatalf("expected empty for non-Bearer auth scheme, got %q", got)
	}
	if got := extractDownstreamAPIKey(http.Header{"Authorization": {"Bearer "}}); got != "" {
		t.Fatalf("expected empty for bare 'Bearer ' with no token, got %q", got)
	}
}

// End-to-end: a 502 + AI API path request must surface the downstream API
// key preview, so operators can correlate "unknown provider for model glm-5"
// errors with the credential the caller presented.
func TestGinLogrusLoggerIncludesDownstreamAPIKeyOnBadGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.WarnLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger(&config.Config{}))
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "unknown provider for model glm-5"}})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"glm-5","temperature":0,"stream":false}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-q2oPV5yCSxUmG2Rfjf9BB52tStEcqIqqqBHK6oxxsepfu6mQpfRvLmG4uVj4qbRI")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	t.Logf("502 log output: %s", logOutput)
	if !strings.Contains(logOutput, "downstream_api_key=") {
		t.Fatalf("expected downstream_api_key segment in 502 log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "Bearer") {
		t.Fatalf("expected Bearer scheme in redacted preview, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "sk-q") {
		t.Fatalf("expected 4-char prefix in redacted preview, got: %s", logOutput)
	}
	const fullSecret = "sk-q2oPV5yCSxUmG2Rfjf9BB52tStEcqIqqqBHK6oxxsepfu6mQpfRvLmG4uVj4qbRI"
	if strings.Contains(logOutput, fullSecret) {
		t.Fatalf("raw API key leaked into log: %s", logOutput)
	}
}
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

// extractDownstreamAPIKey logs the original downstream credential so operators
// can identify the exact configured API key that triggered a WARN/ERROR.
func TestExtractDownstreamAPIKey_ReturnsRawSecret(t *testing.T) {
	const secret = "sk-prod-THISISTHEACTUALSECRETDONOTLEAK"
	cases := []struct {
		name    string
		headers http.Header
		want    string
	}{
		{"bearer", http.Header{"Authorization": {"Bearer " + secret}}, "Bearer(" + secret + ")"},
		{"x-api-key", http.Header{"X-Api-Key": {secret}}, "X-Api-Key(" + secret + ")"},
		{"api-key", http.Header{"Api-Key": {secret}}, "X-Api-Key(" + secret + ")"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := extractDownstreamAPIKey(tc.headers)
			if out != tc.want {
				t.Fatalf("extractDownstreamAPIKey() = %q, want %q", out, tc.want)
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
// key, so operators can correlate "unknown provider for model glm-5"
// errors with the credential the caller presented.
func TestGinLogrusLoggerIncludesRawDownstreamAPIKeyOnWarnAndError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const fullSecret = "sk-q2oPV5yCSxUmG2Rfjf9BB52tStEcqIqqqBHK6oxxsepfu6mQpfRvLmG4uVj4qbRI"
	for _, statusCode := range []int{http.StatusBadRequest, http.StatusBadGateway} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			var logBuffer bytes.Buffer
			log.SetOutput(&logBuffer)
			log.SetLevel(log.WarnLevel)

			engine := gin.New()
			engine.Use(GinLogrusLogger(&config.Config{}))
			engine.POST("/v1/chat/completions", func(c *gin.Context) {
				c.JSON(statusCode, gin.H{"error": gin.H{"message": "request failed"}})
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				bytes.NewReader([]byte(`{"model":"glm-5","temperature":0,"stream":false}`)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+fullSecret)
			recorder := httptest.NewRecorder()

			engine.ServeHTTP(recorder, req)

			logOutput := logBuffer.String()
			t.Logf("%d log output: %s", statusCode, logOutput)
			if !strings.Contains(logOutput, "downstream_api_key=Bearer("+fullSecret+")") {
				t.Fatalf("expected original API key in %d log, got: %s", statusCode, logOutput)
			}
		})
	}
}

package logging

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

func TestGinLogrusRecoveryRepanicsErrAbortHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/abort", func(c *gin.Context) {
		panic(http.ErrAbortHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/abort", nil)
	recorder := httptest.NewRecorder()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("expected panic, got nil")
		}
		err, ok := recovered.(error)
		if !ok {
			t.Fatalf("expected error panic, got %T", recovered)
		}
		if !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("expected ErrAbortHandler, got %v", err)
		}
		if err != http.ErrAbortHandler {
			t.Fatalf("expected exact ErrAbortHandler sentinel, got %v", err)
		}
	}()

	engine.ServeHTTP(recorder, req)
}

func TestGinLogrusRecoveryHandlesRegularPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
}

func TestGinLogrusLoggerAppendsTokenSegment(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.InfoLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger())
	engine.POST("/v1/messages", func(c *gin.Context) {
		detail := usage.Detail{
			InputTokens:  123,
			OutputTokens: 456,
		}
		c.Set("usageDetail", detail)
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{"model":"test"}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	if !bytes.Contains([]byte(logOutput), []byte("tokens in=123 out=456")) {
		t.Fatalf("expected token segment in log, got: %s", logOutput)
	}
}

func TestGinLogrusLoggerSkipsZeroTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.InfoLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger())
	engine.POST("/v1/messages", func(c *gin.Context) {
		detail := usage.Detail{
			InputTokens:  0,
			OutputTokens: 0,
		}
		c.Set("usageDetail", detail)
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{"model":"test"}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	if bytes.Contains([]byte(logOutput), []byte("tokens in=")) {
		t.Fatalf("expected no token segment for zero tokens, got: %s", logOutput)
	}
}

func TestGinLogrusLoggerAppendsTokenSegmentPointerType(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.InfoLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger())
	engine.POST("/v1/messages", func(c *gin.Context) {
		detail := &usage.Detail{
			InputTokens:  789,
			OutputTokens: 321,
		}
		c.Set("usageDetail", detail)
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{"model":"test"}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	if !bytes.Contains([]byte(logOutput), []byte("tokens in=789 out=321")) {
		t.Fatalf("expected token segment with pointer type in log, got: %s", logOutput)
	}
}

func TestGinLogrusLoggerAppendsBillingDecisionSegment(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.InfoLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger())
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set(ginBillingDecisionKey, map[string]string{
			"billing_class": "metered",
			"reason":        "threshold_rule pattern=test-* estimated_tokens=50 target=metered provider=claude auth=test-auth selected_billing_class=metered",
		})
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{"model":"test"}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	if !bytes.Contains([]byte(logOutput), []byte("billing class=metered reason=threshold_rule pattern=test-* estimated_tokens=50 target=metered provider=claude auth=test-auth selected_billing_class=metered")) {
		t.Fatalf("expected billing decision segment in log, got: %s", logOutput)
	}
}

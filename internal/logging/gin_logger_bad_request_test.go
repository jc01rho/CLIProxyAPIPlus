package logging

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

func TestGinLogrusLoggerIncludesRequestAndResponseOnBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.WarnLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger(&config.Config{}))
	engine.POST("/v1/messages", func(c *gin.Context) {
		_ = c.Error(errors.New("local validation failed")).SetType(gin.ErrorTypePrivate)
		c.Set("API_RESPONSE", []byte(`{"error":"bad request detail","why":"missing field"}`))
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request detail", "why": "missing field"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{"model":"claude-opus","messages":[{"role":"user","content":"hi"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	t.Logf("bad request log output: %s", logOutput)
	if !bytes.Contains([]byte(logOutput), []byte(`request=`)) || !bytes.Contains([]byte(logOutput), []byte(`claude-opus`)) {
		t.Fatalf("expected quoted request body in log, got: %s", logOutput)
	}
	if !bytes.Contains([]byte(logOutput), []byte(`response=`)) || !bytes.Contains([]byte(logOutput), []byte(`bad request detail`)) || !bytes.Contains([]byte(logOutput), []byte(`missing field`)) {
		t.Fatalf("expected quoted response body in log, got: %s", logOutput)
	}
	if !bytes.Contains([]byte(logOutput), []byte("local validation failed")) {
		t.Fatalf("expected private error message in log, got: %s", logOutput)
	}
}

func TestGinLogrusLoggerIncludesRequestAndResponseOnSuccessWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.InfoLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger(&config.Config{SDKConfig: config.SDKConfig{RequestLogSuccessBody: true}}))
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("API_RESPONSE", []byte(`{"id":"msg_1","type":"message"}`))
		c.JSON(http.StatusOK, gin.H{"id": "msg_1", "type": "message"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{"model":"claude-sonnet-4-6"}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	if !bytes.Contains([]byte(logOutput), []byte(`request=`)) || !bytes.Contains([]byte(logOutput), []byte(`claude-sonnet-4-6`)) {
		t.Fatalf("expected success request body in log, got: %s", logOutput)
	}
	if !bytes.Contains([]byte(logOutput), []byte(`response=`)) || !bytes.Contains([]byte(logOutput), []byte(`msg_1`)) {
		t.Fatalf("expected success response body in log, got: %s", logOutput)
	}
}

func TestGinLogrusLoggerTruncatesBodiesUsingDetailedAPILogLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.ErrorLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger(&config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogLimit: 80}}))
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		_ = c.Error(errors.New("upstream failed")).SetType(gin.ErrorTypePrivate)
		c.Set("API_RESPONSE", []byte(`{"error":{"message":"`+strings.Repeat("r", 160)+`"}}`))
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "internal"}})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"`+strings.Repeat("x", 220)+`"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	if !strings.Contains(logOutput, `...[truncated]`) {
		t.Fatalf("expected truncated marker in log, got: %s", logOutput)
	}
	if strings.Contains(logOutput, strings.Repeat("x", 120)) {
		t.Fatalf("expected long request message to be truncated, got: %s", logOutput)
	}
	if strings.Contains(logOutput, strings.Repeat("r", 120)) {
		t.Fatalf("expected long response body to be truncated, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, `request=`) || !strings.Contains(logOutput, `response=`) {
		t.Fatalf("expected request and response bodies in log, got: %s", logOutput)
	}
}

func TestGinLogrusLoggerTruncatesPrivateErrorMessageUsingDetailedAPILogLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.ErrorLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger(&config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogLimit: 80}}))
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		_ = c.Error(errors.New("request.message=" + strings.Repeat("m", 220))).SetType(gin.ErrorTypePrivate)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "internal"}})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4.1-mini"}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	logOutput := logBuffer.String()
	if !strings.Contains(logOutput, `request.message=`) {
		t.Fatalf("expected private error message in log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, `...[truncated]`) {
		t.Fatalf("expected truncated marker in private error message, got: %s", logOutput)
	}
	if strings.Contains(logOutput, strings.Repeat("m", 120)) {
		t.Fatalf("expected long private error message to be truncated, got: %s", logOutput)
	}
}

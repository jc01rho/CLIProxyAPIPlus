package logging

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func TestGinLogrusLoggerIncludesRequestAndResponseOnBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.WarnLevel)

	engine := gin.New()
	engine.Use(GinLogrusLogger())
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

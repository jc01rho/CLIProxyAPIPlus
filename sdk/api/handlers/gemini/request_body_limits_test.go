package gemini

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestReadRequestBody_rejectsDecodedZstdRequestBodyOverLimit(t *testing.T) {
	// Given
	gin.SetMode(gin.TestMode)
	payload := bytes.Repeat([]byte("x"), 33<<20)
	compressed := compressZstdBody(t, payload)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/test:generateContent", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "zstd")
	c.Request = req

	// When
	body, errRead := handlers.ReadRequestBody(c)

	// Then
	if body != nil {
		t.Fatalf("decoded body length = %d, want nil body", len(body))
	}
	if !errors.Is(errRead, handlers.ErrRequestBodyTooLarge) {
		t.Fatalf("ReadRequestBody error = %v, want ErrRequestBodyTooLarge", errRead)
	}
	if status := handlers.RequestBodyErrorStatus(errRead); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("RequestBodyErrorStatus() = %d, want %d", status, http.StatusRequestEntityTooLarge)
	}
	if !strings.Contains(errRead.Error(), "decoded request body exceeds 33554432 bytes") {
		t.Fatalf("ReadRequestBody error = %q, want decoded limit", errRead.Error())
	}
}

func TestReadRequestBody_rejectsEncodedRequestBodyOverLimit(t *testing.T) {
	// Given
	gin.SetMode(gin.TestMode)
	payload := bytes.Repeat([]byte("x"), int(handlers.MaxEncodedRequestBodyBytes)+1)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/test:generateContent", bytes.NewReader(payload))
	c.Request = req

	// When
	body, errRead := handlers.ReadRequestBody(c)

	// Then
	if body != nil {
		t.Fatalf("encoded body length = %d, want nil body", len(body))
	}
	if !errors.Is(errRead, handlers.ErrRequestBodyTooLarge) {
		t.Fatalf("ReadRequestBody error = %v, want ErrRequestBodyTooLarge", errRead)
	}
	if status := handlers.RequestBodyErrorStatus(errRead); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("RequestBodyErrorStatus() = %d, want %d", status, http.StatusRequestEntityTooLarge)
	}
	if !strings.Contains(errRead.Error(), "encoded request body exceeds 33554432 bytes") {
		t.Fatalf("ReadRequestBody error = %q, want encoded limit", errRead.Error())
	}
}

func TestReadRequestBody_preservesSupportedZstdRequestBodyWithinLimit(t *testing.T) {
	// Given
	gin.SetMode(gin.TestMode)
	payload := []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`)
	compressed := compressZstdBody(t, payload)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/test:generateContent", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "zstd")
	c.Request = req

	// When
	body, errRead := handlers.ReadRequestBody(c)

	// Then
	if errRead != nil {
		t.Fatalf("ReadRequestBody: %v", errRead)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("decoded body = %q, want %q", string(body), string(payload))
	}
}

func compressZstdBody(t *testing.T, payload []byte) []byte {
	t.Helper()

	var compressed bytes.Buffer
	encoder, errNewWriter := zstd.NewWriter(&compressed)
	if errNewWriter != nil {
		t.Fatalf("zstd.NewWriter: %v", errNewWriter)
	}
	if _, errWrite := encoder.Write(payload); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}
	return compressed.Bytes()
}

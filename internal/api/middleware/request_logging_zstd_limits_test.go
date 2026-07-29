package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	sdkhandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

type closeTrackingCapturedRequestBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingCapturedRequestBody) Close() error {
	b.closed = true
	return nil
}

func TestCaptureRequestInfo_truncatesDecodedZstdRequestBodyForLogWhenExpansionExceedsLimit(t *testing.T) {
	// Given
	gin.SetMode(gin.TestMode)
	payload := bytes.Repeat([]byte("x"), int(sdkhandlers.MaxDecodedRequestBodyBytes)+1)
	compressed := compressZstdRequestLogBody(t, payload)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	originalBody := &closeTrackingCapturedRequestBody{Reader: bytes.NewReader(compressed)}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", originalBody)
	req.Header.Set("Content-Encoding", "zstd")
	c.Request = req

	// When
	info, errCapture := captureRequestInfo(c, true)

	// Then
	if errCapture != nil {
		t.Fatalf("captureRequestInfo: %v", errCapture)
	}
	if len(info.Body) > int(sdkhandlers.MaxDecodedRequestBodyBytes)+64 {
		t.Fatalf("logged decoded body length = %d, want bounded output", len(info.Body))
	}
	if !bytes.Contains(info.Body, []byte("DECOMPRESSED REQUEST BODY TRUNCATED")) {
		t.Fatalf("logged decoded body missing truncation marker")
	}

	restoredBody, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		t.Fatalf("read restored request body: %v", errRead)
	}
	if !bytes.Equal(restoredBody, compressed) {
		t.Fatal("request body was not restored with the original compressed bytes")
	}
	if !originalBody.closed {
		t.Fatal("original request body was not closed after middleware replaced it")
	}
}

func compressZstdRequestLogBody(t *testing.T, payload []byte) []byte {
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

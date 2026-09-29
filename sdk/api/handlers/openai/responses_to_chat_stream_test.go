package openai

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWriteConvertedResponsesChunkAcceptsSSEFrame(t *testing.T) {
	for _, frame := range []string{
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n",
		"event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"answer\"}\r\n\r\n",
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		var param any
		writeConvertedResponsesChunk(ctx, context.Background(), "test-model", []byte(`{"messages":[]}`), []byte(`{"input":[]}`), []byte(frame), &param)
		if body := recorder.Body.String(); !strings.Contains(body, `"content":"answer"`) {
			t.Fatalf("converted chat chunk missing content: %q", body)
		}
	}
}

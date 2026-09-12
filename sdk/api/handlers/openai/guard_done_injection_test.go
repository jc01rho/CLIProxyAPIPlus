package openai

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	responsesconverter "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
)

// TestGuardNotTrippedByDoneInjection pins that the [DONE] marker feeding a
// completed event marks the guard as satisfied. Writing the marker straight
// to the writer left the guard blind, so a healthy turn was followed by a
// spurious error event that clients surfaced as a 502.
func TestGuardNotTrippedByDoneInjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx := context.Background()
	req := []byte(`{"model":"swe-2-high","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	var param any
	guard := &responsesTerminalGuard{}

	chunk := []byte(`{"id":"chatcmpl-devin","object":"chat.completion.chunk","created":0,"model":"swe-2-high","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`)
	guard.write(c, responsesconverter.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "swe-2-high", req, req, chunk, &param))
	guard.write(c, responsesconverter.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "swe-2-high", req, req, []byte("[DONE]"), &param))

	if !guard.emitted {
		t.Fatal("the completed event from the done marker must satisfy the guard")
	}
	guard.ensure(c, "should not appear")
	body := rec.Body.String()
	if strings.Contains(body, "should not appear") {
		t.Fatalf("spurious error appended after a healthy turn:\n%s", body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("terminal event missing:\n%s", body)
	}
}

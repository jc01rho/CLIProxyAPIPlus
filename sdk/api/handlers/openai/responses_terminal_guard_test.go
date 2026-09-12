package openai

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newGuardTestContext returns a gin context writing into a recorder.
func newGuardTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	return c, rec
}

// TestTerminalGuardEmitsWhenNothingWritten pins that a stream producing no
// convertible payload still ends with a terminal event. Previously the
// zero-chunk close wrote only a newline, so clients reported that the stream
// ended before a terminal response event.
func TestTerminalGuardEmitsWhenNothingWritten(t *testing.T) {
	c, rec := newGuardTestContext()
	g := &responsesTerminalGuard{}
	g.ensure(c, "upstream stream closed before any payload")
	body := rec.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("no terminal event emitted: %q", body)
	}
	if !strings.Contains(body, "upstream stream closed before any payload") {
		t.Fatalf("reason missing: %q", body)
	}
}

// TestTerminalGuardStaysSilentAfterCompleted pins that a real terminal event
// suppresses the synthetic one, so a healthy turn is not double-terminated.
func TestTerminalGuardStaysSilentAfterCompleted(t *testing.T) {
	c, rec := newGuardTestContext()
	g := &responsesTerminalGuard{}
	g.write(c, [][]byte{[]byte("event: response.completed"), []byte(`data: {"type":"response.completed"}`)})
	g.ensure(c, "should not appear")
	body := rec.Body.String()
	if strings.Contains(body, "should not appear") {
		t.Fatalf("synthetic terminal event emitted after a real one: %q", body)
	}
	if strings.Count(body, "response.completed") == 0 {
		t.Fatalf("real terminal event lost: %q", body)
	}
}

// TestTerminalGuardDetectsTerminalNames pins the terminal event set.
func TestTerminalGuardDetectsTerminalNames(t *testing.T) {
	terminal := []string{"response.completed", "response.incomplete", "response.failed", "response.error"}
	for _, name := range terminal {
		if !responsesOutputIsTerminal([]byte("event: " + name)) {
			t.Fatalf("%s not recognised as terminal", name)
		}
	}
	if responsesOutputIsTerminal([]byte("event: response.output_text.delta")) {
		t.Fatal("delta must not count as terminal")
	}
}

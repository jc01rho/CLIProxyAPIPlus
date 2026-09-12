package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func omoTestContext(header, value string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if header != "" {
		c.Request.Header.Set(header, value)
	}
	return c
}

// TestIsOmoClientRequest pins detection against the identities the omo agent
// actually sends. omo is a different project from oh-my-pi, so an oh-my-pi
// string must not be treated as omo.
func TestIsOmoClientRequest(t *testing.T) {
	cases := []struct {
		header string
		value  string
		want   bool
	}{
		{"Originator", "omo", true},
		{"Originator", "OMO", true},
		{"User-Agent", "omo/5.0.0-0.beta.56 (linux; node/v24.14.0; x64)", true},
		{"User-Agent", "omo/5.0.0 (darwin; bun/1.4.2; arm64)", true},
		{"User-Agent", "omo-coding-agent", true},
		{"User-Agent", "curl/8.0", false},
		{"User-Agent", "oh-my-pi/1.0", false},
		{"User-Agent", "pi/5.0.0", false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := isOmoClientRequest(omoTestContext(tc.header, tc.value)); got != tc.want {
			t.Fatalf("isOmoClientRequest(%s=%q) = %v, want %v", tc.header, tc.value, got, tc.want)
		}
	}
}

// TestIsOmoClientRequestNilSafe pins the nil guards.
func TestIsOmoClientRequestNilSafe(t *testing.T) {
	if isOmoClientRequest(nil) {
		t.Fatal("nil context must not be detected as omo")
	}
}

// TestOmoGuardEmitsResponseFailed pins that a strict client ends on a terminal
// response event. The Responses API defines response.completed,
// response.incomplete and response.failed as terminal; a bare error event is an
// extension omo does not accept as an ending.
func TestOmoGuardEmitsResponseFailed(t *testing.T) {
	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Originator", "omo")

	g := newResponsesTerminalGuard(c)
	if !g.strict {
		t.Fatal("omo request must build a strict guard")
	}
	g.ensure(c, "upstream stream closed before any payload")

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("strict client did not receive a terminal response event:\n%s", body)
	}
	if !strings.Contains(body, "upstream stream closed before any payload") {
		t.Fatalf("reason missing:\n%s", body)
	}
}

// TestNonOmoGuardKeepsErrorEvent pins that other clients keep the previous
// behaviour.
func TestNonOmoGuardKeepsErrorEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "curl/8.0")

	g := newResponsesTerminalGuard(c)
	g.ensure(c, "boom")
	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("non-omo client should keep the error event:\n%s", rec.Body.String())
	}
}

// TestOmoGuardSilentAfterRealTerminal pins that a healthy turn is untouched.
func TestOmoGuardSilentAfterRealTerminal(t *testing.T) {
	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Originator", "omo")

	g := newResponsesTerminalGuard(c)
	g.write(c, [][]byte{[]byte("event: response.completed")})
	g.ensure(c, "should not appear")
	if strings.Contains(rec.Body.String(), "should not appear") {
		t.Fatalf("synthetic terminal event after a real one:\n%s", rec.Body.String())
	}
}

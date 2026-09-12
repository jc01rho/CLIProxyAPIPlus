package openai

import (
	"net/http"
	"net/http/httptest"
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
		{"User-Agent", "pi/5.0.0-0.beta.56 (linux; node/v24.14.0; x64)", true},
		{"User-Agent", "omo/5.0.0 (darwin; bun/1.4.2; arm64)", true},
		{"User-Agent", "pi-coding-agent", true},
		{"User-Agent", "curl/8.0", false},
		{"User-Agent", "oh-my-pi/1.0", false},
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

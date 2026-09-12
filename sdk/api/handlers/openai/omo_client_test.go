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

// TestIsOmoClientRequest pins detection of the oh-my-pi agent across the
// headers it identifies itself with.
func TestIsOmoClientRequest(t *testing.T) {
	cases := []struct {
		header string
		value  string
		want   bool
	}{
		{"User-Agent", "oh-my-pi/1.2.3", true},
		{"Originator", "omo", true},
		{"X-Client-Name", "ohmypi", true},
		{"User-Agent", "curl/8.0", false},
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

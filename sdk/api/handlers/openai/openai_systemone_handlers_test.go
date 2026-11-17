package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func performSystemOneRequest(body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	h := &OpenAIAPIHandler{}
	router := gin.New()
	router.POST("/v1/systemone", h.SystemOne)

	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func TestSystemOneRejectsMalformedBody(t *testing.T) {
	resp := performSystemOneRequest(`{invalid`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}

func TestSystemOneRequiresModel(t *testing.T) {
	resp := performSystemOneRequest(`{"state":"The checkout is broken.","questions":{"urgent":{"type":"noul","instructions":"x"}}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}

func TestSystemOneRequiresQuestions(t *testing.T) {
	resp := performSystemOneRequest(`{"model":"jev-latest","state":"The checkout is broken."}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}

func TestSystemOneRequiresState(t *testing.T) {
	resp := performSystemOneRequest(`{"model":"jev-latest","questions":{"urgent":{"type":"noul","instructions":"x"}}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}

// A question without a System One type tag used to be forwarded verbatim, so
// every upstream on the fallback chain answered with its own shape error and the
// client waited out the whole chain. The handler now rejects the body up front.
func TestSystemOneRejectsUntaggedQuestion(t *testing.T) {
	resp := performSystemOneRequest(`{"model":"jev-latest","state":"The checkout is broken.","questions":{"q1":{"instructions":"x"}}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "q1") {
		t.Fatalf("error must name the offending question, got: %s", resp.Body.String())
	}
}

func TestSystemOneRejectsUnknownQuestionType(t *testing.T) {
	resp := performSystemOneRequest(`{"model":"jev-latest","state":"s","questions":{"q1":{"type":"bool","instructions":"x"}}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}

func TestSystemOneRejectsEmptyQuestions(t *testing.T) {
	resp := performSystemOneRequest(`{"model":"jev-latest","state":"s","questions":{}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "at least one question") {
		t.Fatalf("empty questions must say so, got: %s", resp.Body.String())
	}
}

func TestInvalidSystemOneQuestionAcceptsTaggedQuestions(t *testing.T) {
	body := []byte(`{"model":"jev-latest","state":"s","questions":{"a":{"type":"noul"},"b":{"type":"choice","criteria":{"x":"y"}},"c":{"type":"score","criteria":{"low":"0"}}}}`)
	if got := invalidSystemOneQuestion(body); got != "" {
		t.Fatalf("invalidSystemOneQuestion(%s) = %q, want no offending question", body, got)
	}
}

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

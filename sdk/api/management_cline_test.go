package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// TestManagementTokenRequester_ExposesClineToken verifies that the SDK's
// ManagementTokenRequester exposes the Cline token-request endpoint so
// external embedders can drive the same flow Management Center uses.
func TestManagementTokenRequester_ExposesClineToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	requester := NewManagementTokenRequester(&sdkconfig.Config{}, manager)

	router := gin.New()
	router.GET("/cline-auth-url", requester.RequestClineToken)

	req := httptest.NewRequest(http.MethodGet, "/cline-auth-url", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want ok", resp.Status)
	}
	if resp.State == "" {
		t.Fatalf("expected state to be non-empty")
	}
	if !strings.Contains(resp.URL, "state="+resp.State) {
		t.Fatalf("auth URL %q does not embed state %q", resp.URL, resp.State)
	}
	if !strings.Contains(resp.URL, "client_type=extension") {
		t.Fatalf("auth URL %q does not include 9Router client_type=extension", resp.URL)
	}
}

package amp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// closeNotifierResponseRecorder wraps httptest.ResponseRecorder to implement
// http.CloseNotifier, which is required by httputil.ReverseProxy.
// Note: http.CloseNotifier is deprecated in favor of Request.Context(), but
// httputil.ReverseProxy still checks for it internally.
type closeNotifierResponseRecorder struct {
	*httptest.ResponseRecorder
	closeNotifyChan chan bool
}

func newCloseNotifierResponseRecorder() *closeNotifierResponseRecorder {
	return &closeNotifierResponseRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closeNotifyChan:  make(chan bool, 1),
	}
}

// CloseNotify implements http.CloseNotifier
func (c *closeNotifierResponseRecorder) CloseNotify() <-chan bool {
	return c.closeNotifyChan
}

func TestFallbackHandler_ModelMapping_PreservesThinkingSuffixAndRewritesResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-amp-fallback", "codex", []*registry.ModelInfo{
		{ID: "test/gpt-5.2", OwnedBy: "openai", Type: "codex"},
	})
	defer reg.UnregisterClient("test-client-amp-fallback")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gpt-5.2", To: "test/gpt-5.2"},
	})

	fallback := NewFallbackHandlerWithMapper(func() *httputil.ReverseProxy { return nil }, mapper, nil)

	handler := func(c *gin.Context) {
		var req struct {
			Model string `json:"model"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"model":      req.Model,
			"seen_model": req.Model,
		})
	}

	r := gin.New()
	r.POST("/chat/completions", fallback.WrapHandler(handler))

	reqBody := []byte(`{"model":"gpt-5.2(xhigh)"}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	var resp struct {
		Model     string `json:"model"`
		SeenModel string `json:"seen_model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response JSON: %v", err)
	}

	if resp.Model != "gpt-5.2(xhigh)" {
		t.Errorf("Expected response model gpt-5.2(xhigh), got %s", resp.Model)
	}
	if resp.SeenModel != "test/gpt-5.2(xhigh)" {
		t.Errorf("Expected handler to see test/gpt-5.2(xhigh), got %s", resp.SeenModel)
	}
}

// Test 4.1: No provider available - forwards to ampcode.com
func TestFallbackHandler_NoProviderAvailable_ForwardsToProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Create a mock backend server to simulate ampcode.com
	backendCalled := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"source": "ampcode.com",
			"model":  "unknown-model",
		})
	}))
	defer backend.Close()

	// Create reverse proxy to the mock backend
	backendURL, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(backendURL)

	// Create fallback handler with no model mappings
	fallback := NewFallbackHandlerWithMapper(func() *httputil.ReverseProxy { return proxy }, nil, nil)

	// Handler that should NOT be called when proxy is used
	handler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"source": "local"})
	}

	r := gin.New()
	r.POST("/chat/completions", fallback.WrapHandler(handler))

	// Request with a model that has no provider configured
	reqBody := []byte(`{"model":"unknown-model-xyz"}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	// Use closeNotifierResponseRecorder to satisfy httputil.ReverseProxy's CloseNotifier requirement
	w := newCloseNotifierResponseRecorder()
	r.ServeHTTP(w, req)

	// Should have forwarded to the proxy (ampcode.com)
	if !backendCalled {
		t.Error("Expected request to be forwarded to ampcode.com proxy, but it wasn't")
	}
}

// Test 4.2: Has provider available - uses local provider
func TestFallbackHandler_HasProviderAvailable_UsesLocalProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	// Register a client with a known model
	reg.RegisterClient("test-client-local-provider", "gemini", []*registry.ModelInfo{
		{ID: "gemini-pro", OwnedBy: "google", Type: "gemini"},
	})
	defer reg.UnregisterClient("test-client-local-provider")

	// Create a mock backend that should NOT be called
	backendCalled := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(backendURL)

	// Create fallback handler
	fallback := NewFallbackHandlerWithMapper(func() *httputil.ReverseProxy { return proxy }, nil, nil)

	// Handler that SHOULD be called for local provider
	localHandlerCalled := false
	handler := func(c *gin.Context) {
		localHandlerCalled = true
		c.JSON(http.StatusOK, gin.H{"source": "local", "model": "gemini-pro"})
	}

	r := gin.New()
	r.POST("/chat/completions", fallback.WrapHandler(handler))

	// Request with a model that HAS a local provider
	reqBody := []byte(`{"model":"gemini-pro"}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Should have used local handler, NOT the proxy
	if backendCalled {
		t.Error("Expected request to use local provider, but it was forwarded to proxy")
	}
	if !localHandlerCalled {
		t.Error("Expected local handler to be called, but it wasn't")
	}

	// Verify response
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if resp["source"] != "local" {
		t.Errorf("Expected source 'local', got '%s'", resp["source"])
	}
}

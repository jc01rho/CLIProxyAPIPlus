package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func setupTestRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	return r
}

func createTempConfigFile(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	initialConfig := []byte("routing:\n  strategy: round-robin\n")
	if err := os.WriteFile(configPath, initialConfig, 0644); err != nil {
		t.Fatalf("failed to create temp config: %v", err)
	}
	return configPath
}

func TestGetRoutingMode(t *testing.T) {
	tests := []struct {
		name         string
		configMode   string
		expectedMode string
	}{
		{"empty mode returns provider-based", "", "provider-based"},
		{"provider-based mode", "provider-based", "provider-based"},
		{"key-based mode", "key-based", "key-based"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Routing: config.RoutingConfig{
					Mode: tt.configMode,
				},
			}
			h := &Handler{cfg: cfg}
			r := setupTestRouter(h)
			r.GET("/routing/mode", h.GetRoutingMode)

			req := httptest.NewRequest(http.MethodGet, "/routing/mode", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", w.Code)
			}

			var resp map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}

			if resp["mode"] != tt.expectedMode {
				t.Errorf("expected mode %q, got %q", tt.expectedMode, resp["mode"])
			}
		})
	}
}

func TestPutRoutingMode(t *testing.T) {
	tests := []struct {
		name           string
		inputValue     string
		expectedStatus int
		expectedMode   string
	}{
		{"valid key-based", "key-based", http.StatusOK, "key-based"},
		{"valid provider-based", "provider-based", http.StatusOK, "provider-based"},
		{"alias key", "key", http.StatusOK, "key-based"},
		{"alias provider", "provider", http.StatusOK, "provider-based"},
		{"invalid mode", "invalid-mode", http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := createTempConfigFile(t)
			cfg := &config.Config{}
			h := &Handler{cfg: cfg, configFilePath: configPath}
			r := setupTestRouter(h)
			r.PUT("/routing/mode", h.PutRoutingMode)

			body, _ := json.Marshal(map[string]string{"value": tt.inputValue})
			req := httptest.NewRequest(http.MethodPut, "/routing/mode", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if tt.expectedStatus == http.StatusOK && cfg.Routing.Mode != tt.expectedMode {
				t.Errorf("expected config mode %q, got %q", tt.expectedMode, cfg.Routing.Mode)
			}
		})
	}
}

func TestGetFallbackModels(t *testing.T) {
	tests := []struct {
		name           string
		configModels   map[string]string
		expectedModels map[string]string
	}{
		{"nil models returns empty map", nil, map[string]string{}},
		{"empty models returns empty map", map[string]string{}, map[string]string{}},
		{"with models", map[string]string{"model-a": "model-b"}, map[string]string{"model-a": "model-b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Routing: config.RoutingConfig{
					FallbackModels: tt.configModels,
				},
			}
			h := &Handler{cfg: cfg}
			r := setupTestRouter(h)
			r.GET("/fallback/models", h.GetFallbackModels)

			req := httptest.NewRequest(http.MethodGet, "/fallback/models", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", w.Code)
			}

			var resp map[string]map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}

			models := resp["fallback-models"]
			if len(models) != len(tt.expectedModels) {
				t.Errorf("expected %d models, got %d", len(tt.expectedModels), len(models))
			}
		})
	}
}

func TestPutFallbackModels(t *testing.T) {
	configPath := createTempConfigFile(t)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: configPath}
	r := setupTestRouter(h)
	r.PUT("/fallback/models", h.PutFallbackModels)

	inputModels := map[string]string{"model-a": "model-b", "model-c": "model-d"}
	body, _ := json.Marshal(map[string]interface{}{"value": inputModels})
	req := httptest.NewRequest(http.MethodPut, "/fallback/models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if len(cfg.Routing.FallbackModels) != 2 {
		t.Errorf("expected 2 models, got %d", len(cfg.Routing.FallbackModels))
	}

	if cfg.Routing.FallbackModels["model-a"] != "model-b" {
		t.Errorf("expected model-a -> model-b, got %s", cfg.Routing.FallbackModels["model-a"])
	}
}

func TestGetFallbackChain(t *testing.T) {
	tests := []struct {
		name          string
		configChain   []string
		expectedChain []string
	}{
		{"nil chain returns empty array", nil, []string{}},
		{"empty chain returns empty array", []string{}, []string{}},
		{"with chain", []string{"model-a", "model-b"}, []string{"model-a", "model-b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Routing: config.RoutingConfig{
					FallbackChain: tt.configChain,
				},
			}
			h := &Handler{cfg: cfg}
			r := setupTestRouter(h)
			r.GET("/fallback/chain", h.GetFallbackChain)

			req := httptest.NewRequest(http.MethodGet, "/fallback/chain", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", w.Code)
			}

			var resp map[string][]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}

			chain := resp["fallback-chain"]
			if len(chain) != len(tt.expectedChain) {
				t.Errorf("expected %d items, got %d", len(tt.expectedChain), len(chain))
			}
		})
	}
}

func TestPutFallbackChain(t *testing.T) {
	configPath := createTempConfigFile(t)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: configPath}
	r := setupTestRouter(h)
	r.PUT("/fallback/chain", h.PutFallbackChain)

	inputChain := []string{"model-a", "model-b", "model-c"}
	body, _ := json.Marshal(map[string]interface{}{"value": inputChain})
	req := httptest.NewRequest(http.MethodPut, "/fallback/chain", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if len(cfg.Routing.FallbackChain) != 3 {
		t.Errorf("expected 3 items, got %d", len(cfg.Routing.FallbackChain))
	}

	if cfg.Routing.FallbackChain[0] != "model-a" {
		t.Errorf("expected first item model-a, got %s", cfg.Routing.FallbackChain[0])
	}
}

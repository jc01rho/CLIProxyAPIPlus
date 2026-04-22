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

func TestPutConfigYAML_AppliesRuntimeConfigCallback(t *testing.T) {
	configPath := createTempConfigFile(t)
	cfg := &config.Config{}
	h := NewHandler(cfg, configPath, nil)

	var applied *config.Config
	h.SetOnConfigApplied(func(updated *config.Config) {
		applied = updated
	})

	r := setupTestRouter(h)
	r.PUT("/config-yaml", h.PutConfigYAML)

	body := []byte("api-key-ip-blacklist:\n  failure-threshold: 5\n  failure-window: 15m\n  block-duration: 2h\n")
	req := httptest.NewRequest(http.MethodPut, "/config-yaml", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	if applied == nil {
		t.Fatalf("expected runtime config callback to be invoked")
	}
	if applied.APIKeyIPBlacklist.FailureThreshold != 5 {
		t.Fatalf("expected applied failure threshold 5, got %d", applied.APIKeyIPBlacklist.FailureThreshold)
	}
	if applied.APIKeyIPBlacklist.FailureWindow != "15m" {
		t.Fatalf("expected applied failure window 15m, got %q", applied.APIKeyIPBlacklist.FailureWindow)
	}
	if applied.APIKeyIPBlacklist.BlockDuration != "2h" {
		t.Fatalf("expected applied block duration 2h, got %q", applied.APIKeyIPBlacklist.BlockDuration)
	}
	if h.apiKeyIPBlacklist.Policy().FailureThreshold != 5 {
		t.Fatalf("expected in-memory blacklist policy to update via callback, got %d", h.apiKeyIPBlacklist.Policy().FailureThreshold)
	}
}

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

func TestGetTokenThresholdRules(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			TokenThresholdRules: []config.TokenThresholdRule{{
				ModelPattern: "gpt-*",
				MaxTokens:    100,
				BillingClass: config.BillingClassMetered,
				Enabled:      true,
			}},
		},
	}
	h := &Handler{cfg: cfg}
	r := setupTestRouter(h)
	r.GET("/routing/token-threshold-rules", h.GetTokenThresholdRules)

	req := httptest.NewRequest(http.MethodGet, "/routing/token-threshold-rules", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	var resp map[string][]config.TokenThresholdRule
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(resp["token-threshold-rules"]) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(resp["token-threshold-rules"]))
	}
}

func TestPutTokenThresholdRules(t *testing.T) {
	configPath := createTempConfigFile(t)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: configPath}
	r := setupTestRouter(h)
	r.PUT("/routing/token-threshold-rules", h.PutTokenThresholdRules)

	body, _ := json.Marshal(map[string]any{"value": []map[string]any{{
		"model-pattern": "gpt-*",
		"max-tokens": 100,
		"billing-class": "metered",
	}}})
	req := httptest.NewRequest(http.MethodPut, "/routing/token-threshold-rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if len(cfg.Routing.TokenThresholdRules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(cfg.Routing.TokenThresholdRules))
	}
	if cfg.Routing.TokenThresholdRules[0].BillingClass != config.BillingClassMetered {
		t.Fatalf("expected billing class metered, got %q", cfg.Routing.TokenThresholdRules[0].BillingClass)
	}
}

func TestPutTokenThresholdRulesWithMinTokensRoundTrip(t *testing.T) {
	configPath := createTempConfigFile(t)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: configPath}
	r := setupTestRouter(h)
	r.PUT("/routing/token-threshold-rules", h.PutTokenThresholdRules)
	r.GET("/routing/token-threshold-rules", h.GetTokenThresholdRules)

	body, _ := json.Marshal(map[string]any{"value": []map[string]any{
		{
			"model-pattern": "opus-*",
			"min-tokens": 0,
			"max-tokens": 1500,
			"billing-class": "metered",
		},
		{
			"model-pattern": "opus-*",
			"min-tokens": 1501,
			"max-tokens": 2000,
			"billing-class": "per-request",
		},
	}})
	req := httptest.NewRequest(http.MethodPut, "/routing/token-threshold-rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT: expected status 200, got %d", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/routing/token-threshold-rules", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("GET: expected status 200, got %d", w2.Code)
	}

	var resp struct {
		Rules []map[string]any `json:"token-threshold-rules"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse GET response: %v", err)
	}
	if len(resp.Rules) != 2 {
		t.Fatalf("expected 2 rules in GET, got %d", len(resp.Rules))
	}

	for i, rule := range resp.Rules {
		minTok, ok := rule["min-tokens"]
		if !ok {
			if i == 0 {
				continue
			}
			t.Fatalf("rule %d missing min-tokens field", i)
		}
		if minTok != float64(i*1501) {
			t.Fatalf("rule %d: expected min-tokens %d, got %v", i, i*1501, minTok)
		}
	}
}

func TestPutTokenThresholdRulesLowerOnlyRoundTrip(t *testing.T) {
	configPath := createTempConfigFile(t)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: configPath}
	r := setupTestRouter(h)
	r.PUT("/routing/token-threshold-rules", h.PutTokenThresholdRules)
	r.GET("/routing/token-threshold-rules", h.GetTokenThresholdRules)

	body, _ := json.Marshal(map[string]any{"value": []map[string]any{
		{
			"model-pattern": "opus-*",
			"max-tokens": 1500,
			"billing-class": "metered",
		},
		{
			"model-pattern": "opus-*",
			"min-tokens": 2001,
			"billing-class": "per-request",
		},
	}})
	req := httptest.NewRequest(http.MethodPut, "/routing/token-threshold-rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT: expected status 200, got %d", w.Code)
	}
	if len(cfg.Routing.TokenThresholdRules) != 2 {
		t.Fatalf("expected 2 rules after PUT, got %d", len(cfg.Routing.TokenThresholdRules))
	}

	lowerOnly := cfg.Routing.TokenThresholdRules[1]
	if lowerOnly.MinTokens != 2001 {
		t.Fatalf("expected lower-only min-tokens 2001, got %d", lowerOnly.MinTokens)
	}
	if lowerOnly.MaxTokens != 0 {
		t.Fatalf("expected lower-only max-tokens 0, got %d", lowerOnly.MaxTokens)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/routing/token-threshold-rules", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	var resp struct {
		Rules []map[string]any `json:"token-threshold-rules"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse GET: %v", err)
	}
	if len(resp.Rules) != 2 {
		t.Fatalf("expected 2 rules in GET, got %d", len(resp.Rules))
	}

	lowerResp := resp.Rules[1]
	minTok, ok := lowerResp["min-tokens"]
	if !ok {
		t.Fatal("lower-only rule missing min-tokens in GET response")
	}
	if minTok != float64(2001) {
		t.Fatalf("expected min-tokens 2001, got %v", minTok)
	}
	if _, hasMax := lowerResp["max-tokens"]; hasMax {
		t.Fatal("lower-only rule should not have max-tokens in GET response (omitempty)")
	}
}

func TestPutTokenThresholdRulesDropsEmptyRule(t *testing.T) {
	configPath := createTempConfigFile(t)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: configPath}
	r := setupTestRouter(h)
	r.PUT("/routing/token-threshold-rules", h.PutTokenThresholdRules)

	body, _ := json.Marshal(map[string]any{"value": []map[string]any{
		{"model-pattern": "valid", "max-tokens": 100, "billing-class": "metered"},
		{"model-pattern": "empty", "billing-class": "metered"},
		{"model-pattern": "lower", "min-tokens": 50, "billing-class": "per-request"},
	}})
	req := httptest.NewRequest(http.MethodPut, "/routing/token-threshold-rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if len(cfg.Routing.TokenThresholdRules) != 2 {
		t.Fatalf("expected 2 rules (empty dropped), got %d", len(cfg.Routing.TokenThresholdRules))
	}
	for _, r := range cfg.Routing.TokenThresholdRules {
		if r.ModelPattern == "empty" {
			t.Fatal("expected empty rule to be dropped")
		}
	}
}

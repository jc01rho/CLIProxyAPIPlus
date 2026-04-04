package management

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// normalizeRoutingMode normalizes the routing mode value.
// Supported values: "" (default, provider-based), "key-based" (model-only key).
func normalizeRoutingMode(mode string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "", "provider-based", "provider":
		return "provider-based", true
	case "key-based", "key", "model-only":
		return "key-based", true
	default:
		return "", false
	}
}

// GetRoutingMode returns the current routing mode.
func (h *Handler) GetRoutingMode(c *gin.Context) {
	mode, ok := normalizeRoutingMode(h.cfg.Routing.Mode)
	if !ok {
		c.JSON(200, gin.H{"mode": strings.TrimSpace(h.cfg.Routing.Mode)})
		return
	}
	c.JSON(200, gin.H{"mode": mode})
}

// PutRoutingMode updates the routing mode.
func (h *Handler) PutRoutingMode(c *gin.Context) {
	var body struct {
		Value *string `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	normalized, ok := normalizeRoutingMode(*body.Value)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mode"})
		return
	}
	h.cfg.Routing.Mode = normalized
	h.persist(c)
}

// GetFallbackModels returns the fallback models configuration.
func (h *Handler) GetFallbackModels(c *gin.Context) {
	models := h.cfg.Routing.FallbackModels
	if models == nil {
		models = make(map[string]string)
	}
	c.JSON(200, gin.H{"fallback-models": models})
}

// PutFallbackModels updates the fallback models configuration.
func (h *Handler) PutFallbackModels(c *gin.Context) {
	var body struct {
		Value map[string]string `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if body.Value == nil {
		body.Value = make(map[string]string)
	}
	h.cfg.Routing.FallbackModels = body.Value
	h.persist(c)
}

// GetFallbackChain returns the fallback chain configuration.
func (h *Handler) GetFallbackChain(c *gin.Context) {
	chain := h.cfg.Routing.FallbackChain
	if chain == nil {
		chain = []string{}
	}
	c.JSON(200, gin.H{"fallback-chain": chain})
}

// PutFallbackChain updates the fallback chain configuration.
func (h *Handler) PutFallbackChain(c *gin.Context) {
	var body struct {
		Value []string `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if body.Value == nil {
		body.Value = []string{}
	}
	h.cfg.Routing.FallbackChain = body.Value
	h.persist(c)
}

// GetTokenThresholdRules returns the token-threshold routing configuration.
func (h *Handler) GetTokenThresholdRules(c *gin.Context) {
	rules := h.cfg.Routing.TokenThresholdRules
	if rules == nil {
		rules = []config.TokenThresholdRule{}
	}
	c.JSON(200, gin.H{"token-threshold-rules": rules})
}

// PutTokenThresholdRules updates the token-threshold routing configuration.
func (h *Handler) PutTokenThresholdRules(c *gin.Context) {
	var body struct {
		Value []config.TokenThresholdRule `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if body.Value == nil {
		body.Value = []config.TokenThresholdRule{}
	}
	tmpCfg := *h.cfg
	tmpCfg.Routing.TokenThresholdRules = append([]config.TokenThresholdRule(nil), body.Value...)
	tmpCfg.SanitizeTokenThresholdRules()
	h.cfg.Routing.TokenThresholdRules = tmpCfg.Routing.TokenThresholdRules
	h.persist(c)
}

func normalizeBillingClassValue(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "metered":
		return normalized
	case "per_request", "per-request":
		return "per-request"
	default:
		return ""
	}
}

func applyBillingClassToConfigAPIKeys(cfg *config.Config) {
	if cfg == nil {
		return
	}
	for i := range cfg.GeminiKey {
		cfg.GeminiKey[i].BillingClass = config.BillingClass(normalizeBillingClassValue(string(cfg.GeminiKey[i].BillingClass)))
	}
	for i := range cfg.ClaudeKey {
		cfg.ClaudeKey[i].BillingClass = config.BillingClass(normalizeBillingClassValue(string(cfg.ClaudeKey[i].BillingClass)))
	}
	for i := range cfg.CodexKey {
		cfg.CodexKey[i].BillingClass = config.BillingClass(normalizeBillingClassValue(string(cfg.CodexKey[i].BillingClass)))
	}
	for i := range cfg.VertexCompatAPIKey {
		cfg.VertexCompatAPIKey[i].BillingClass = config.BillingClass(normalizeBillingClassValue(string(cfg.VertexCompatAPIKey[i].BillingClass)))
	}
	for i := range cfg.OpenAICompatibility {
		cfg.OpenAICompatibility[i].BillingClass = config.BillingClass(normalizeBillingClassValue(string(cfg.OpenAICompatibility[i].BillingClass)))
	}
}

func normalizeTokenThresholdRuleBillingClass(rule *config.TokenThresholdRule) {
	if rule == nil {
		return
	}
	rule.BillingClass = config.BillingClass(normalizeBillingClassValue(string(rule.BillingClass)))
	if rule.ModelPattern != "" {
		rule.ModelPattern = strings.TrimSpace(rule.ModelPattern)
	}
	if rule.Enabled == false && rule.MaxTokens > 0 && rule.BillingClass != "" {
		// zero-value bool from YAML/JSON means enabled unless explicitly false is desired via UI,
		// keep current zero-value semantics simple by auto-enabling meaningful rules.
		rule.Enabled = true
	}
	if rule.ModelPattern != "" {
		rule.ModelPattern = strings.Trim(strings.ReplaceAll(rule.ModelPattern, "\\", "/"), " ")
		if base := filepath.Base(rule.ModelPattern); base != "." && base != "/" {
			rule.ModelPattern = rule.ModelPattern
		}
	}
}

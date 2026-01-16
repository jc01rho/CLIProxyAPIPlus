package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
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

package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// GetFallbackModels returns the model-specific fallback configuration.
func (h *Handler) GetFallbackModels(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(200, gin.H{"fallback-models": map[string]string{}})
		return
	}
	models := h.cfg.Routing.FallbackModels
	if models == nil {
		models = map[string]string{}
	}
	c.JSON(200, gin.H{"fallback-models": models})
}

// PutFallbackModels updates the model-specific fallback configuration.
func (h *Handler) PutFallbackModels(c *gin.Context) {
	var body struct {
		Value map[string]string `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	// Validate the configuration
	testCfg := &config.RoutingConfig{FallbackModels: body.Value}
	if err := config.ValidateRoutingConfig(testCfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.cfg.Routing.FallbackModels = body.Value
	h.persist(c)
}

// GetFallbackChain returns the fallback chain configuration.
func (h *Handler) GetFallbackChain(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(200, gin.H{"fallback-chain": []string{}})
		return
	}
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
	// Validate the configuration
	testCfg := &config.RoutingConfig{FallbackChain: body.Value}
	if err := config.ValidateRoutingConfig(testCfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.cfg.Routing.FallbackChain = body.Value
	h.persist(c)
}

// GetProviderPriority returns the model-specific provider priority configuration.
func (h *Handler) GetProviderPriority(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(200, gin.H{"provider-priority": map[string][]string{}})
		return
	}
	priority := h.cfg.Routing.ProviderPriority
	if priority == nil {
		priority = map[string][]string{}
	}
	c.JSON(200, gin.H{"provider-priority": priority})
}

// PutProviderPriority updates the model-specific provider priority configuration.
func (h *Handler) PutProviderPriority(c *gin.Context) {
	var body struct {
		Value map[string][]string `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.cfg.Routing.ProviderPriority = body.Value
	h.persist(c)
}

// GetProviderPriorityForModel returns the provider priority for a specific model.
func (h *Handler) GetProviderPriorityForModel(c *gin.Context) {
	model := c.Param("model")
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model parameter required"})
		return
	}
	if h == nil || h.cfg == nil || h.cfg.Routing.ProviderPriority == nil {
		c.JSON(200, gin.H{"model": model, "providers": []string{}})
		return
	}
	providers := h.cfg.Routing.ProviderPriority[model]
	if providers == nil {
		providers = []string{}
	}
	c.JSON(200, gin.H{"model": model, "providers": providers})
}

// PutProviderPriorityForModel updates the provider priority for a specific model.
func (h *Handler) PutProviderPriorityForModel(c *gin.Context) {
	model := c.Param("model")
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model parameter required"})
		return
	}
	var body struct {
		Value []string `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if h.cfg.Routing.ProviderPriority == nil {
		h.cfg.Routing.ProviderPriority = make(map[string][]string)
	}
	h.cfg.Routing.ProviderPriority[model] = body.Value
	h.persist(c)
}

// GetProviderOrder returns the global provider order configuration.
func (h *Handler) GetProviderOrder(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(200, gin.H{"provider-order": []string{}})
		return
	}
	order := h.cfg.Routing.ProviderOrder
	if order == nil {
		order = []string{}
	}
	c.JSON(200, gin.H{"provider-order": order})
}

// PutProviderOrder updates the global provider order configuration.
func (h *Handler) PutProviderOrder(c *gin.Context) {
	var body struct {
		Value []string `json:"value"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.cfg.Routing.ProviderOrder = body.Value
	h.persist(c)
}

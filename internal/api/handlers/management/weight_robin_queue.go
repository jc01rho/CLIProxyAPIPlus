package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (h *Handler) GetWeightRobinQueue(c *gin.Context) {
	manager := h.authManager
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager not available"})
		return
	}

	selector := manager.GetSelector()
	if selector == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "selector not available"})
		return
	}

	weightedSelector, ok := selector.(*auth.WeightedRobinSelector)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "current selector is not weight-robin"})
		return
	}

	model := c.Query("model")
	allAuths := manager.List()
	snapshot := weightedSelector.QueueState(model, allAuths)
	c.JSON(http.StatusOK, snapshot)
}

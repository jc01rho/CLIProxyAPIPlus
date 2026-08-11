package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// GetStaticModelDefinitions returns runtime model metadata for a given channel,
// falling back to the static catalog when no active client has registered models.
// Channel is provided via path param (:channel) or query param (?channel=...).
func (h *Handler) GetStaticModelDefinitions(c *gin.Context) {
	channel := strings.TrimSpace(c.Param("channel"))
	if channel == "" {
		channel = strings.TrimSpace(c.Query("channel"))
	}
	if channel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel is required"})
		return
	}

	normalizedChannel := strings.ToLower(strings.TrimSpace(channel))
	models := registry.GetGlobalRegistry().GetAvailableModelsByProvider(normalizedChannel)
	if len(models) == 0 {
		models = registry.GetStaticModelDefinitionsByChannel(normalizedChannel)
	}
	if models == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": normalizedChannel,
		"models":  models,
	})
}

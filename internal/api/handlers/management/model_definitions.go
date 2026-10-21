package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
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
	// Plugin-declared static models win for plugin channels; otherwise the fork
	// prefers runtime registry models over the static catalog.
	models := h.pluginStaticModelDefinitionsByChannel(normalizedChannel)
	if len(models) == 0 {
		models = sortedRegistryModelDefinitions(normalizedChannel)
	}
	if len(models) == 0 {
		models = registry.GetStaticModelDefinitionsByChannel(normalizedChannel)
	}
	if len(models) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": normalizedChannel,
		"models":  models,
	})
}

func (h *Handler) pluginStaticModelDefinitionsByChannel(channel string) []*registry.ModelInfo {
	var host *pluginhost.Host
	if h != nil {
		h.mu.Lock()
		host = h.pluginHost
		h.mu.Unlock()
	}
	if host == nil {
		return nil
	}
	return host.ModelsForProvider(channel)
}

func sortedRegistryModelDefinitions(channel string) []*registry.ModelInfo {
	models := registry.GetGlobalRegistry().GetAvailableModelsByProvider(channel)
	if len(models) > 1 {
		sort.Slice(models, func(i, j int) bool {
			if models[i] == nil {
				return models[j] != nil
			}
			if models[j] == nil {
				return false
			}
			return models[i].ID < models[j].ID
		})
	}
	return models
}

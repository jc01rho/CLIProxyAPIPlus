package executor

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// FilterKiloModels keeps advertised kilo/openrouter models whose ID or
// display name contains "free" (for example openrouter/free or *:free).
func FilterKiloModels(models []*registry.ModelInfo) []*registry.ModelInfo {
	return filterModelsContaining(models, "free")
}

// FilterCursorModels keeps advertised cursor models whose ID or display
// name contains "free", "composer", "grok", "opus", "sonnet", or "kimi-k3".
func FilterCursorModels(models []*registry.ModelInfo) []*registry.ModelInfo {
	return filterModelsContaining(models, "free", "composer", "grok", "opus", "sonnet", "kimi-k3")
}

// FilterKiroModels keeps advertised kiro models whose ID or display name
// contains "claude".
func FilterKiroModels(models []*registry.ModelInfo) []*registry.ModelInfo {
	return filterModelsContaining(models, "claude")
}

func filterModelsContaining(models []*registry.ModelInfo, needles ...string) []*registry.ModelInfo {
	if len(models) == 0 {
		return models
	}
	filtered := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if modelCatalogContainsAny(model, needles...) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func modelCatalogContainsAny(model *registry.ModelInfo, needles ...string) bool {
	if model == nil {
		return false
	}
	id := strings.ToLower(model.ID)
	name := strings.ToLower(model.DisplayName)
	for _, needle := range needles {
		if strings.Contains(id, needle) || strings.Contains(name, needle) {
			return true
		}
	}
	return false
}

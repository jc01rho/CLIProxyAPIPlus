// Package registry provides model definitions for various AI service providers.
package registry

// GetClineModels returns the Cline model definitions
func GetClineModels() []*ModelInfo {
	return []*ModelInfo{
		// --- Base Models ---
		{
			ID:                  "cline/auto",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "cline",
			Type:                "cline",
			DisplayName:         "Cline Auto",
			Description:         "Automatic model selection by Cline",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
	}
}

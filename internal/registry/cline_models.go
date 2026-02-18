// Package registry provides model definitions for various AI service providers.
package registry

// GetClineModels returns the Cline model definitions
func GetClineModels() []*ModelInfo {
	return []*ModelInfo{
		// --- Auto Model ---
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
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		// --- Free Models (available via Cline) ---
		{
			ID:                  "anthropic/claude-sonnet-4.6",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "cline",
			Type:                "cline",
			DisplayName:         "Claude Sonnet 4.6 (via Cline)",
			Description:         "Anthropic Claude Sonnet 4.6 via Cline (Free)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kwaipilot/kat-coder-pro",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "cline",
			Type:                "cline",
			DisplayName:         "KAT Coder Pro (via Cline)",
			Description:         "KwaiPilot KAT Coder Pro via Cline (Free)",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
		},
		{
			ID:                  "z-ai/glm-5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "cline",
			Type:                "cline",
			DisplayName:         "GLM-5 (via Cline)",
			Description:         "Z-AI GLM-5 via Cline (Free)",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
		},
		{
			ID:                  "minimax/minimax-m2.5",
			Object:              "model",
			Created:             1770825600,
			OwnedBy:             "cline",
			Type:                "cline",
			DisplayName:         "MiniMax M2.5 (via Cline)",
			Description:         "MiniMax M2.5 via Cline (Free)",
			ContextLength:       204800,
			MaxCompletionTokens: 128000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
	}
}

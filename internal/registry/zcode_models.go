// Package registry provides model definitions for various AI service providers.
package registry

// GetZcodeModels returns the GLM ZCode (unofficial) model catalog. The zcode
// provider logs in via ZCode's OAuth but auto-provisions a real Z.AI API key
// and calls api.z.ai directly (Anthropic-compatible messages API).
func GetZcodeModels() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:                        "glm-5.2",
			Object:                    "model",
			Type:                      "claude",
			DisplayName:               "GLM-5.2",
			ContextLength:             1000000,
			MaxCompletionTokens:       131072,
			SupportedInputModalities:  []string{"TEXT"},
			SupportedOutputModalities: []string{"TEXT"},
		},
	}
}

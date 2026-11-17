package registry

// DeepAI model tiers exposed by the deepai.org chat reverse proxy. The upstream
// catalog is not discoverable (no /models endpoint), so this static table is the
// authoritative client-facing list.
const (
	deepAICreated = int64(1790035200)
)

// GetDeepAIModels returns the DeepAI chat model catalog.
func GetDeepAIModels() []*ModelInfo {
	return []*ModelInfo{
		deepAIStaticModel("deepai-standard", "DeepAI Standard", 32000),
		deepAIStaticModel("deepai-genius", "DeepAI Genius", 32000),
		deepAIStaticModel("deepai-super-genius", "DeepAI Super Genius", 128000),
		deepAIStaticModel("gpt-4o-mini", "DeepAI GPT-4o mini (alias)", 32000),
	}
}

func deepAIStaticModel(id, displayName string, contextLength int) *ModelInfo {
	return &ModelInfo{
		ID:                  id,
		Name:                id,
		Object:              "model",
		Created:             deepAICreated,
		OwnedBy:             "deepai",
		Type:                "deepai",
		DisplayName:         displayName,
		ContextLength:       contextLength,
		MaxCompletionTokens: 16384,
		SupportedEndpoints:  []string{"/chat/completions"},
	}
}

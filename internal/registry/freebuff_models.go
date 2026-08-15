package registry

// GetFreebuffModels returns the conservative static Freebuff catalog.
// Agent IDs remain configurable because Freebuff has changed base2/base3 roots.
func GetFreebuffModels() []*ModelInfo {
	return []*ModelInfo{
		freebuffStaticModel("deepseek/deepseek-v4-pro", "DeepSeek V4 Pro", &ThinkingSupport{Levels: []string{"low", "high", "max"}}),
		freebuffStaticModel("deepseek/deepseek-v4-flash", "DeepSeek V4 Flash", &ThinkingSupport{Levels: []string{"low", "high", "max"}}),
		freebuffStaticModel("mimo/mimo-v2.5", "MiMo 2.5", nil),
		freebuffStaticModel("minimax/minimax-m3", "MiniMax M3", nil),
		freebuffStaticModel("z-ai/glm-5.2", "GLM 5.2", nil),
		freebuffStaticModel("openai/gpt-5.6-luna", "GPT-5.6 Luna", &ThinkingSupport{Levels: []string{"high"}}),
		freebuffStaticModel("anthropic/claude-fable-5", "Claude Fable 5", nil),
	}
}

func freebuffStaticModel(id, displayName string, thinking *ThinkingSupport) *ModelInfo {
	return &ModelInfo{
		ID:            id,
		Object:        "model",
		OwnedBy:       "freebuff",
		Type:          "freebuff",
		DisplayName:   displayName,
		ContextLength: 200000,
		Thinking:      thinking,
	}
}

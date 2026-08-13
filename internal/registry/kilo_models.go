// Package registry provides model definitions for various AI service providers.
package registry

// GetKiloModels returns the Kilo model definitions. The first entry (kilo/auto)
// is always preserved; the remaining entries mirror the OmniRoute kilocode
// passthrough catalog so the free tier is visible even without a live /models
// fetch.
func GetKiloModels() []*ModelInfo {
	return []*ModelInfo{
		// --- Base Models ---
		{
			ID:                  "kilo/auto",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "kilo",
			Type:                "kilo",
			DisplayName:         "Kilo Auto",
			Description:         "Automatic model selection by Kilo",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		// --- OpenRouter passthrough catalog (mirrors OmniRoute kilocode registry) ---
		{ID: "openrouter/free", DisplayName: "Free Models Router", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "qwen/qwen3.6-plus", DisplayName: "Qwen3.6 Plus", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "qwen/qwen3.5-397b-a17b", DisplayName: "Qwen3.5 397B A17B", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "openai/gpt-5.5", DisplayName: "GPT-5.5", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "openai/gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "anthropic/claude-opus-4.7", DisplayName: "Claude Opus 4.7", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "anthropic/claude-sonnet-4.6", DisplayName: "Claude Sonnet 4.6", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "anthropic/claude-haiku-4.5", DisplayName: "Claude Haiku 4.5", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "google/gemini-3.1-pro-preview", DisplayName: "Gemini 3.1 Pro", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "google/gemini-3-flash-preview", DisplayName: "Gemini 3 Flash", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "google/gemini-3.1-flash-lite", DisplayName: "Gemini 3.1 Flash Lite", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "deepseek/deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "deepseek/deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
		{ID: "moonshotai/kimi-k2.6", DisplayName: "Kimi K2.6", OwnedBy: "kilo", Type: "kilo", Object: "model", Created: 1732752000},
	}
}

// GetKiloGatewayModels returns the kilo-gateway model definitions. Auth is
// optional for this gateway (free tier answers without an Authorization header).
func GetKiloGatewayModels() []*ModelInfo {
	return []*ModelInfo{
		{ID: "kilo-auto/frontier", DisplayName: "Kilo Auto Frontier", OwnedBy: "kilo-gateway", Type: "kilo-gateway", Object: "model", Created: 1732752000},
		{ID: "kilo-auto/balanced", DisplayName: "Kilo Auto Balanced", OwnedBy: "kilo-gateway", Type: "kilo-gateway", Object: "model", Created: 1732752000},
		{ID: "kilo-auto/free", DisplayName: "Kilo Auto Free", OwnedBy: "kilo-gateway", Type: "kilo-gateway", Object: "model", Created: 1732752000},
		{ID: "nvidia/nemotron-3-super-120b-a12b:free", DisplayName: "Nemotron 3 Super 120B (Free)", OwnedBy: "kilo-gateway", Type: "kilo-gateway", Object: "model", Created: 1732752000},
		{ID: "minimax/minimax-m2.5:free", DisplayName: "MiniMax M2.5 (Free)", OwnedBy: "kilo-gateway", Type: "kilo-gateway", Object: "model", Created: 1732752000},
		{ID: "arcee-ai/trinity-large-preview:free", DisplayName: "Trinity Large Preview (Free)", OwnedBy: "kilo-gateway", Type: "kilo-gateway", Object: "model", Created: 1732752000},
	}
}

// Package registry provides model definitions for various AI service providers.
package registry

// GetCommandCodeModels returns the fallback CommandCode catalog captured from
// https://api.commandcode.ai/provider/v1/models. Live registration prefers that
// endpoint and only uses this list when the fetch fails or returns nothing.
func GetCommandCodeModels() []*ModelInfo {
	return []*ModelInfo{
		commandCodeStaticModel("claude-sonnet-5", "Claude Sonnet 5", 1000000, 1786690443),
		commandCodeStaticModel("claude-sonnet-4-6", "Claude Sonnet 4.6", 1000000, 1786690443),
		commandCodeStaticModel("claude-fable-5", "Claude Fable 5", 1000000, 1786690443),
		commandCodeStaticModel("claude-opus-5", "Claude Opus 5", 1000000, 1786690443),
		commandCodeStaticModel("claude-opus-4-8", "Claude Opus 4.8", 1000000, 1786690443),
		commandCodeStaticModel("claude-opus-4-7", "Claude Opus 4.7", 1000000, 1786690443),
		commandCodeStaticModel("claude-haiku-4-5-20251001", "Claude Haiku 4.5", 200000, 1786690443),
		commandCodeStaticModel("gpt-5.6-sol", "GPT-5.6 Sol", 1050000, 1786690443),
		commandCodeStaticModel("gpt-5.6-terra", "GPT-5.6 Terra", 1050000, 1786690443),
		commandCodeStaticModel("gpt-5.6-luna", "GPT-5.6 Luna", 1050000, 1786690443),
		commandCodeStaticModel("gpt-5.5", "GPT-5.5", 200000, 1786690443),
		commandCodeStaticModel("gpt-5.4", "GPT-5.4", 400000, 1786690443),
		commandCodeStaticModel("gpt-5.3-codex", "GPT-5.3 Codex", 400000, 1786690443),
		commandCodeStaticModel("gpt-5.4-mini", "GPT-5.4 Mini", 400000, 1786690443),
		commandCodeStaticModel("deepseek/deepseek-v4-pro", "DeepSeek V4 Pro (latest)", 1000000, 1786690443),
		commandCodeStaticModel("deepseek/deepseek-v4-flash", "DeepSeek V4 Flash (latest)", 1000000, 1786690443),
		commandCodeStaticModel("moonshotai/Kimi-K3", "Kimi K3", 1000000, 1786690443),
		commandCodeStaticModel("moonshotai/Kimi-K2.7-Code", "Kimi K2.7 Code", 256000, 1786690443),
		commandCodeStaticModel("moonshotai/Kimi-K2.7-Code-Highspeed", "Kimi K2.7 Code HighSpeed", 262000, 1786690443),
		commandCodeStaticModel("moonshotai/Kimi-K2.6", "Kimi K2.6", 256000, 1786690443),
		commandCodeStaticModel("moonshotai/Kimi-K2.5", "Kimi K2.5", 256000, 1786690443),
		commandCodeStaticModel("zai-org/GLM-5.2", "GLM-5.2", 1000000, 1786690443),
		commandCodeStaticModel("zai-org/GLM-5.2-Fast", "GLM-5.2 Fast", 1000000, 1786690443),
		commandCodeStaticModel("zai-org/GLM-5.1", "GLM-5.1", 200000, 1786690443),
		commandCodeStaticModel("zai-org/GLM-5", "GLM-5", 200000, 1786690443),
		commandCodeStaticModel("MiniMaxAI/MiniMax-M3", "MiniMax M3", 1000000, 1786690443),
		commandCodeStaticModel("MiniMaxAI/MiniMax-M2.7", "MiniMax M2.7", 200000, 1786690443),
		commandCodeStaticModel("MiniMaxAI/MiniMax-M2.5", "MiniMax M2.5", 200000, 1786690443),
		commandCodeStaticModel("xiaomi/mimo-v2.5-pro", "MiMo V2.5 Pro", 1000000, 1786690443),
		commandCodeStaticModel("xiaomi/mimo-v2.5", "MiMo V2.5", 1000000, 1786690443),
		commandCodeStaticModel("Qwen/Qwen3.8-Max", "Qwen 3.8 Max", 1000000, 1786690443),
		commandCodeStaticModel("Qwen/Qwen3.7-Max", "Qwen 3.7 Max", 1000000, 1786690443),
		commandCodeStaticModel("Qwen/Qwen3.7-Plus", "Qwen 3.7 Plus", 1000000, 1786690443),
		commandCodeStaticModel("Qwen/Qwen3.7-Flash", "Qwen 3.7 Flash", 1000000, 1786690443),
		commandCodeStaticModel("Qwen/Qwen3.6-Max-Preview", "Qwen 3.6 Max Preview", 200000, 1786690443),
		commandCodeStaticModel("Qwen/Qwen3.6-Plus", "Qwen 3.6 Plus", 200000, 1786690443),
		commandCodeStaticModel("stepfun/Step-3.7-Flash", "Step 3.7 Flash", 256000, 1786690443),
		commandCodeStaticModel("stepfun/Step-3.5-Flash", "Step 3.5 Flash", 1000000, 1786690443),
		commandCodeStaticModel("tencent/hy3-paid", "Tencent Hy3", 262144, 1786690443),
		commandCodeStaticModel("google/gemini-3.7-flash", "Gemini 3.7 Flash", 1048576, 1786690443),
		commandCodeStaticModel("google/gemini-3.6-flash", "Gemini 3.6 Flash", 1000000, 1786690443),
		commandCodeStaticModel("google/gemini-3.5-flash", "Gemini 3.5 Flash", 1000000, 1786690443),
		commandCodeStaticModel("google/gemini-3.5-flash-lite", "Gemini 3.5 Flash Lite", 1000000, 1786690443),
		commandCodeStaticModel("google/gemini-3.1-flash-lite", "Gemini 3.1 Flash Lite", 1000000, 1786690443),
		commandCodeStaticModel("sakana/fugu-ultra", "Fugu Ultra", 1000000, 1786690443),
		commandCodeStaticModel("nvidia/nemotron-3-ultra-550b-a55b", "Nemotron 3 Ultra", 1000000, 1786690443),
		commandCodeStaticModel("thinkingmachines/inkling", "Inkling", 256000, 1786690443),
		commandCodeStaticModel("thinkingmachines/inkling-small", "Inkling Small", 1000000, 1786690443),
		commandCodeStaticModel("poolside/laguna-s-2.1-free", "Laguna S 2.1", 256000, 1786690443),
		commandCodeStaticModel("meta/muse-spark-1.1", "Muse Spark 1.1", 1048576, 1786690443),
		commandCodeStaticModel("meta/muse-spark-1.2", "Muse Spark 1.2", 1048576, 1786690443),
		commandCodeStaticModel("meta/muse-spark-1.2-contributor", "Muse Spark 1.2 Contributor", 1048576, 1786690443),
		commandCodeStaticModel("xai/grok-4.5", "Grok 4.5", 500000, 1786690443),
		commandCodeStaticModel("xai/grok-4.6", "Grok 4.6", 500000, 1786690443),
	}
}

func commandCodeStaticModel(id, displayName string, contextLength int, created int64) *ModelInfo {
	return &ModelInfo{
		ID:            id,
		Object:        "model",
		Created:       created,
		OwnedBy:       "commandcode",
		Type:          "commandcode",
		DisplayName:   displayName,
		Name:          id,
		ContextLength: contextLength,
	}
}

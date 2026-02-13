// Package registry provides Kilocode model conversion utilities.
// This file handles converting dynamic Kilocode API model lists to the internal ModelInfo format,
// and filtering for free models based on pricing information.
package registry

import (
	"strconv"
	"strings"
	"time"
)

// KilocodeAPIModel represents a model from Kilocode API response.
// This structure mirrors the OpenRouter-compatible API format used by Kilocode.
type KilocodeAPIModel struct {
	// ID is the unique identifier for the model (e.g., "devstral-2-2512")
	ID string `json:"id"`
	// Name is the human-readable name
	Name string `json:"name"`
	// Pricing contains cost information for prompt and completion tokens
	Pricing struct {
		// Prompt is the cost per prompt token (string format, e.g., "0" for free)
		Prompt string `json:"prompt"`
		// Completion is the cost per completion token (string format, e.g., "0" for free)
		Completion string `json:"completion"`
	} `json:"pricing"`
	// ContextLength is the maximum context window size
	ContextLength int `json:"context_length"`
}

// KilocodeAPIResponse represents the full API response from Kilocode models endpoint.
type KilocodeAPIResponse struct {
	// Data contains the list of available models
	Data []*KilocodeAPIModel `json:"data"`
}

// DefaultKilocodeThinkingSupport defines the default thinking configuration for Kilocode models.
// All Kilocode models support thinking with the following budget range.
var DefaultKilocodeThinkingSupport = &ThinkingSupport{
	Min:            1024,  // Minimum thinking budget tokens
	Max:            32000, // Maximum thinking budget tokens
	ZeroAllowed:    true,  // Allow disabling thinking with 0
	DynamicAllowed: true,  // Allow dynamic thinking budget (-1)
}

// DefaultKilocodeContextLength is the default context window size for Kilocode models.
const DefaultKilocodeContextLength = 128000

// DefaultKilocodeMaxCompletionTokens is the default max completion tokens for Kilocode models.
const DefaultKilocodeMaxCompletionTokens = 32000

// ConvertKilocodeAPIModels converts Kilocode API models to internal ModelInfo format.
// It performs the following transformations:
//   - Normalizes model ID (e.g., devstral-2-2512 → kilocode-devstral-2-2512)
//   - Filters for free models only (pricing.prompt == "0" && pricing.completion == "0")
//   - Adds default thinking support metadata
//   - Sets context length from API or uses default if not provided
//
// Parameters:
//   - kilocodeModels: List of models from Kilocode API response
//
// Returns:
//   - []*ModelInfo: Converted model information list (free models only, filtered by allowed providers)
func ConvertKilocodeAPIModels(kilocodeModels []*KilocodeAPIModel) []*ModelInfo {
	if len(kilocodeModels) == 0 {
		return nil
	}

	now := time.Now().Unix()
	result := make([]*ModelInfo, 0, len(kilocodeModels))

	for _, km := range kilocodeModels {
		if km == nil {
			continue
		}

		if km.ID == "" {
			continue
		}

		if !isFreeModel(km) {
			continue
		}

		if !isAllowedKilocodeProvider(km.ID) {
			continue
		}

		normalizedID := normalizeKilocodeModelID(km.ID)

		info := &ModelInfo{
			ID:                  normalizedID,
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         generateKilocodeDisplayName(km.Name, normalizedID),
			Description:         generateKilocodeDescription(km.Name, normalizedID),
			ContextLength:       getKilocodeContextLength(km.ContextLength),
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		}

		result = append(result, info)
	}

	return result
}

// allowedKilocodeProviders defines which model providers are allowed to be listed.
var allowedKilocodeProviders = []string{
	"deepseek/",
	"minimax/",
	"openai/gpt-oss",
	"tngtech/",
	"upstage/",
	"z-ai/",
}

// isAllowedKilocodeProvider checks if a model ID belongs to an allowed provider.
func isAllowedKilocodeProvider(modelID string) bool {
	idLower := strings.ToLower(modelID)
	for _, prefix := range allowedKilocodeProviders {
		if strings.HasPrefix(idLower, prefix) {
			return true
		}
	}
	return false
}

// isFreeModel checks if a Kilocode model is free based on pricing information.
// A model is considered free if both prompt and completion costs are zero.
// Handles various pricing formats: "0", "0.0", "0.0000000", etc.
func isFreeModel(model *KilocodeAPIModel) bool {
	if model == nil {
		return false
	}

	promptPrice, err1 := strconv.ParseFloat(strings.TrimSpace(model.Pricing.Prompt), 64)
	completionPrice, err2 := strconv.ParseFloat(strings.TrimSpace(model.Pricing.Completion), 64)

	if err1 != nil || err2 != nil {
		return false
	}

	return promptPrice == 0 && completionPrice == 0
}

// normalizeKilocodeModelID converts Kilocode API model IDs to internal format.
// Transformation rules:
//   - Adds "kilocode-" prefix if not present
//   - Handles special cases and ensures consistent naming
//
// Examples:
//   - "devstral-2-2512" → "kilocode-devstral-2-2512"
//   - "trinity-large-preview" → "kilocode-trinity-large-preview"
//   - "kilocode-mimo-v2-flash" → "kilocode-mimo-v2-flash" (unchanged)
func normalizeKilocodeModelID(modelID string) string {
	if modelID == "" {
		return ""
	}

	// Trim whitespace
	modelID = strings.TrimSpace(modelID)

	// Add kilocode- prefix if not present
	if !strings.HasPrefix(modelID, "kilocode-") {
		modelID = "kilocode-" + modelID
	}

	return modelID
}

// generateKilocodeDisplayName creates a human-readable display name.
// Uses the API-provided model name if available, otherwise generates from ID.
func generateKilocodeDisplayName(modelName, normalizedID string) string {
	if modelName != "" && modelName != normalizedID {
		return "Kilocode " + modelName
	}

	// Generate from normalized ID by removing kilocode- prefix and formatting
	displayID := strings.TrimPrefix(normalizedID, "kilocode-")
	// Capitalize first letter of each word
	words := strings.Split(displayID, "-")
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return "Kilocode " + strings.Join(words, " ")
}

// generateKilocodeDescription creates a description for Kilocode models.
func generateKilocodeDescription(modelName, normalizedID string) string {
	if modelName != "" && modelName != normalizedID {
		return "Kilocode AI model: " + modelName + " (Free tier)"
	}

	displayID := strings.TrimPrefix(normalizedID, "kilocode-")
	return "Kilocode AI model: " + displayID + " (Free tier)"
}

// getKilocodeContextLength returns the context length, using default if not provided.
func getKilocodeContextLength(contextLength int) int {
	if contextLength > 0 {
		return contextLength
	}
	return DefaultKilocodeContextLength
}

// ResolveKilocodeModelAlias normalizes model names for Kilocode API.
// It strips the "kilocode-" prefix if present and passes through the model name.
//
// Model alias resolution (e.g., "kimi" → "moonshotai/kimi-k2.5:free") should be
// configured via openai-compatibility.models[] in config.yaml, NOT hardcoded here.
//
// Examples:
//   - "kilocode-moonshotai/kimi-k2.5:free" → "moonshotai/kimi-k2.5:free"
//   - "moonshotai/kimi-k2.5:free" → "moonshotai/kimi-k2.5:free" (unchanged)
//   - "kimi" → "kimi" (unchanged - config alias handles this BEFORE executor)
func ResolveKilocodeModelAlias(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return alias
	}

	// Strip kilocode- prefix if present
	return strings.TrimPrefix(alias, "kilocode-")
}

// GetKilocodeModels returns a static list of free Kilocode models.
// The Kilocode API does not support the /models endpoint (returns 405 Method Not Allowed),
// so we maintain a static list of known free models.
// Only includes: deepseek, minimax, gpt-oss, chimera, upstage, z-ai
func GetKilocodeModels() []*ModelInfo {
	now := int64(1738368000) // 2025-02-01
	return []*ModelInfo{
		// DeepSeek
		{
			ID:                  "kilocode-deepseek/deepseek-r1-0528:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode DeepSeek R1 0528 (Free)",
			Description:         "DeepSeek R1 0528 (Free tier)",
			ContextLength:       163840,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		// MiniMax
		{
			ID:                  "kilocode-minimax/minimax-m2.1:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode MiniMax M2.1 (Free)",
			Description:         "MiniMax M2.1 (Free tier)",
			ContextLength:       204800,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		{
			ID:                  "kilocode-minimax/minimax-m2.5:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode MiniMax M2.5 (Free)",
			Description:         "MiniMax M2.5 (Free tier)",
			ContextLength:       204800,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		// GPT-OSS
		{
			ID:                  "kilocode-openai/gpt-oss-20b:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode GPT-OSS 20B (Free)",
			Description:         "OpenAI GPT-OSS 20B (Free tier)",
			ContextLength:       131072,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		{
			ID:                  "kilocode-openai/gpt-oss-120b:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode GPT-OSS 120B (Free)",
			Description:         "OpenAI GPT-OSS 120B (Free tier)",
			ContextLength:       131072,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		// Chimera (TNG Tech)
		{
			ID:                  "kilocode-tngtech/deepseek-r1t-chimera:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode DeepSeek R1T Chimera (Free)",
			Description:         "TNG DeepSeek R1T Chimera (Free tier)",
			ContextLength:       163840,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		{
			ID:                  "kilocode-tngtech/deepseek-r1t2-chimera:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode DeepSeek R1T2 Chimera (Free)",
			Description:         "TNG DeepSeek R1T2 Chimera (Free tier)",
			ContextLength:       163840,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		{
			ID:                  "kilocode-tngtech/tng-r1t-chimera:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode TNG R1T Chimera (Free)",
			Description:         "TNG R1T Chimera (Free tier)",
			ContextLength:       163840,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		// Upstage
		{
			ID:                  "kilocode-upstage/solar-pro-3:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode Solar Pro 3 (Free)",
			Description:         "Upstage Solar Pro 3 (Free tier)",
			ContextLength:       128000,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		// Z-AI (GLM)
		{
			ID:                  "kilocode-z-ai/glm-4.5-air:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode GLM 4.5 Air (Free)",
			Description:         "Z.AI GLM 4.5 Air (Free tier)",
			ContextLength:       131072,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
		{
			ID:                  "kilocode-z-ai/glm-5:free",
			Object:              "model",
			Created:             now,
			OwnedBy:             "kilocode",
			Type:                "kilocode",
			DisplayName:         "Kilocode GLM 5 (Free)",
			Description:         "Z.AI GLM 5 (Free tier)",
			ContextLength:       202800,
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			Thinking:            cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		},
	}
}

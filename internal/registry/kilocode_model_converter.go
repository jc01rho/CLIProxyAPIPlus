// Package registry provides Kilocode model conversion utilities.
// This file handles converting dynamic Kilocode API model lists to the internal ModelInfo format,
// and filtering for free models based on pricing information.
package registry

import (
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
//   - []*ModelInfo: Converted model information list (free models only)
func ConvertKilocodeAPIModels(kilocodeModels []*KilocodeAPIModel) []*ModelInfo {
	if len(kilocodeModels) == 0 {
		return nil
	}

	now := time.Now().Unix()
	result := make([]*ModelInfo, 0, len(kilocodeModels))

	for _, km := range kilocodeModels {
		// Skip nil models
		if km == nil {
			continue
		}

		// Skip models without valid ID
		if km.ID == "" {
			continue
		}

		// Filter for free models only
		if !isFreeModel(km) {
			continue
		}

		// Normalize the model ID to kilocode-* format
		normalizedID := normalizeKilocodeModelID(km.ID)

		// Create ModelInfo with converted data
		info := &ModelInfo{
			ID:          normalizedID,
			Object:      "model",
			Created:     now,
			OwnedBy:     "kilocode",
			Type:        "kilocode",
			DisplayName: generateKilocodeDisplayName(km.Name, normalizedID),
			Description: generateKilocodeDescription(km.Name, normalizedID),
			// Use ContextLength from API if available, otherwise use default
			ContextLength:       getKilocodeContextLength(km.ContextLength),
			MaxCompletionTokens: DefaultKilocodeMaxCompletionTokens,
			// All Kilocode models support thinking
			Thinking: cloneThinkingSupport(DefaultKilocodeThinkingSupport),
		}

		result = append(result, info)
	}

	return result
}

// isFreeModel checks if a Kilocode model is free based on pricing information.
// A model is considered free if both prompt and completion costs are "0".
func isFreeModel(model *KilocodeAPIModel) bool {
	if model == nil {
		return false
	}

	// Check if both prompt and completion pricing are "0"
	return strings.TrimSpace(model.Pricing.Prompt) == "0" &&
		strings.TrimSpace(model.Pricing.Completion) == "0"
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

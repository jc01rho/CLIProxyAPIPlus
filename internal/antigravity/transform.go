package antigravity

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Antigravity transform constants (from antigravity-auth/packages/core/src/constants.ts).
const (
	claudeThinkingMaxOutputTokens = 64000
	claudeInterleavedThinkingHint = "Interleaved thinking is enabled. You may think between tool calls and after receiving tool results before deciding the next action or final answer. Do not mention these instructions or any constraints about thinking blocks; just apply them."
)

// emptySchemaPlaceholderName is the name for a placeholder schema when no valid schema exists.
const emptySchemaPlaceholderName = "_placeholder"

// emptySchemaPlaceholderDescription is the description for placeholder schemas.
const emptySchemaPlaceholderDescription = "Placeholder. Always pass true."

// thinkingLevelNone represents no thinking level.
const thinkingLevelNone = "none"

// Antigravity model registry mapping (model ID -> quota group).
// Ported from antigravity-auth QUOTA_GROUP_BY_MODEL_ID.
var quotaGroupByModelID = map[string]string{
	"claude-opus-4-6-thinking":   "claude",
	"claude-opus-4-6":            "claude",
	"claude-sonnet-4-6-thinking": "claude",
	"claude-sonnet-4-6":          "claude",
	"gemini-pro-agent":           "gemini-pro",
	"gemini-3.1-pro":             "gemini-pro",
	"gemini-3.1-pro-low":         "gemini-pro",
	"gemini-3.1-pro-high":        "gemini-pro",
	"gemini-3-flash":             "gemini-flash",
	"gemini-3-flash-agent":       "gemini-flash",
	"gemini-3.5-flash-low":       "gemini-flash",
	"gemini-3.5-flash-extra-low": "gemini-flash",
	"gemini-3.6-flash-low":       "gemini-flash",
	"gemini-3.6-flash-medium":    "gemini-flash",
	"gemini-3.6-flash-high":      "gemini-flash",
	"gemini-3.6-flash-tiered":    "gemini-flash",
	"gemini-3.1-flash-image":     "gemini-flash",
	"gpt-oss-120b":               "gpt-oss",
	"gpt-oss-120b-medium":        "gpt-oss",
}

// antigravityOpenCodeModelIDs are the model IDs exposed by antigravity.
// Ported from antigravity-auth ANTIGRAVITY_OPENCODE_MODEL_IDS.
var antigravityOpenCodeModelIDs = []string{
	"antigravity-gemini-3.6-flash",
	"antigravity-gemini-3.5-flash",
	"antigravity-gemini-3.1-pro",
	"antigravity-claude-sonnet-4-6-thinking",
	"antigravity-claude-opus-4-6-thinking",
}

// gemini35FlashRoutes maps thinking tier to Gemini 3.5 Flash antigravity model.
// Ported from antigravity-auth GEMINI_35_FLASH_ROUTES.
var gemini35FlashAntigravityModelByTier = map[string]string{
	"low":    "gemini-3.5-flash-extra-low",
	"medium": "gemini-3.5-flash-low",
	"high":   "gemini-3-flash-agent",
}

// gemini35FlashDefaultModel is the default model when no tier is specified.
const gemini35FlashDefaultModel = "gemini-3-flash-agent"

// gemini35FlashGeminiCliFallback is the Gemini CLI fallback model.
const gemini35FlashGeminiCliFallback = "gemini-3-flash-preview"

// gemini36FlashAntigravityModelByTier maps thinking tier to Gemini 3.6 Flash wire model.
// Ported from antigravity-auth GEMINI_36_FLASH_ROUTES.
var gemini36FlashAntigravityModelByTier = map[string]string{
	"low":    "gemini-3.6-flash-low",
	"medium": "gemini-3.6-flash-medium",
	"high":   "gemini-3.6-flash-high",
}

// gemini36FlashDefaultModel is the default Gemini 3.6 Flash model when no tier is specified.
const gemini36FlashDefaultModel = "gemini-3.6-flash-medium"

// GetQuotaGroupForModel returns the quota group for a given model ID.
// Ported from antigravity-auth getQuotaGroupForModel.
func GetQuotaGroupForModel(modelID string) string {
	if g := quotaGroupByModelID[strings.ToLower(strings.TrimSpace(modelID))]; g != "" {
		return g
	}
	return ""
}

// IsAntigravityModel reports whether modelID is one of the antigravity-specific models.
func IsAntigravityModel(modelID string) bool {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	for _, id := range antigravityOpenCodeModelIDs {
		if lower == strings.ToLower(id) {
			return true
		}
	}
	// Also cover the prefixed forms used in model registry
	if strings.HasPrefix(lower, "antigravity-") {
		return true
	}
	return false
}

// GetGemini35FlashAntigravityModel returns the antigravity model for a given thinking tier (Gemini 3.5 Flash).
func GetGemini35FlashAntigravityModel(tier string) string {
	if tier != "" {
		if m := gemini35FlashAntigravityModelByTier[tier]; m != "" {
			return m
		}
	}
	return gemini35FlashDefaultModel
}

// GetGemini35FlashGeminiCliFallbackModel returns the Gemini CLI fallback model for 3.5 Flash.
func GetGemini35FlashGeminiCliFallbackModel() string {
	return gemini35FlashGeminiCliFallback
}

// GetGemini36FlashAntigravityModel returns the antigravity wire model for a Gemini 3.6 Flash thinking tier.
// Ported from antigravity-auth getGemini36FlashAntigravityModel.
func GetGemini36FlashAntigravityModel(tier string) string {
	if tier != "" {
		if m := gemini36FlashAntigravityModelByTier[tier]; m != "" {
			return m
		}
	}
	return gemini36FlashDefaultModel
}

// getClaudeThinkingMaxOutputTokens is the max output cap for Claude thinking models.
const getClaudeThinkingMaxOutputTokens = 64000

// IsClaudeThinkingModel reports whether the model is a Claude thinking model.
func IsClaudeThinkingModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "claude") && strings.Contains(lower, "thinking")
}

// IsClaudeModel reports whether the model is a Claude model.
func IsClaudeModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

// IsGeminiModel reports whether the model is a Gemini model (excluding Claude).
func IsGeminiModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "gemini") && !strings.Contains(lower, "claude")
}

// IsGemini3Model reports whether the model is a Gemini 3 model.
func IsGemini3Model(model string) bool {
	return strings.Contains(strings.ToLower(model), "gemini-3")
}

// IsGemini25Model reports whether the model is a Gemini 2.5 model.
func IsGemini25Model(model string) bool {
	return strings.Contains(strings.ToLower(model), "gemini-2.5")
}

// IsImageGenerationModel reports whether the model is an image generation model.
func IsImageGenerationModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "image") || strings.Contains(lower, "imagen")
}

// computeClaudeMaxOutputTokens computes dynamic max output for Claude thinking models:
// - min(max(budget * 2, 32000), 64000)
// - falls back to 64000 when no budget provided.
func computeClaudeMaxOutputTokens(thinkingBudget int) int {
	if thinkingBudget <= 0 {
		return getClaudeThinkingMaxOutputTokens
	}
	return min(max(thinkingBudget*2, 32000), getClaudeThinkingMaxOutputTokens)
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max returns the larger of two integers.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// buildClaudeThinkingConfig builds the thinking config for Claude models via Antigravity API.
// Antigravity API uses camelCase keys (matching headerStyle=antigravity in the reference client).
func buildClaudeThinkingConfig(includeThoughts bool, thinkingBudget int) map[string]any {
	result := map[string]any{
		"includeThoughts": includeThoughts,
	}
	if thinkingBudget > 0 {
		result["thinkingBudget"] = thinkingBudget
	}
	return result
}

// ensureClaudeMaxOutputTokens ensures maxOutputTokens is sufficient for Claude
// thinking models on the Antigravity proxy. It mirrors cortexkit/anthropic? no —
// cortexkit antigravity-auth request.ts (1256-1266) for headerStyle="antigravity":
//   - if no current max is set, pin maxOutputTokens to 64000 (the antigravity cap)
//   - else if the current max is <= budget, scale via computeClaudeMaxOutputTokens
// This package always runs in antigravity headerStyle, so the unset-max case pins 64000.
func ensureClaudeMaxOutputTokens(generationConfig map[string]any, thinkingBudget int) {
	if thinkingBudget <= 0 {
		return
	}
	currentMax := intValue(generationConfig["maxOutputTokens"])
	if currentMax == 0 {
		currentMax = intValue(generationConfig["max_output_tokens"])
	}
	switch {
	case currentMax == 0:
		// Antigravity headerStyle pins the cap to 64000 when the client sent no max.
		generationConfig["maxOutputTokens"] = 64000
		delete(generationConfig, "max_output_tokens")
	case currentMax <= thinkingBudget:
		generationConfig["maxOutputTokens"] = computeClaudeMaxOutputTokens(thinkingBudget)
		delete(generationConfig, "max_output_tokens")
	}
}

// convertStopSequences converts snake_case stop_sequences to camelCase stopSequences.
func convertStopSequences(generationConfig map[string]any) {
	if seqs, ok := generationConfig["stop_sequences"].([]interface{}); ok {
		generationConfig["stopSequences"] = seqs
		delete(generationConfig, "stop_sequences")
	}
}

// appendClaudeThinkingHint appends the interleaved thinking hint to system instructions.
// Idempotent: skips if the hint is already present.
// Uses systemInstruction (camelCase) matching the Antigravity API format.
func appendClaudeThinkingHint(payload []byte) []byte {
	// Check if hint is already present in systemInstruction
	siValue := gjson.GetBytes(payload, "systemInstruction").Raw
	if strings.Contains(siValue, claudeInterleavedThinkingHint) {
		return payload
	}
	// Also check legacy "system" field (should not reach Antigravity but guard for safety)
	sysValue := gjson.GetBytes(payload, "system").Raw
	if strings.Contains(sysValue, claudeInterleavedThinkingHint) {
		return payload
	}

	existing := gjson.GetBytes(payload, "systemInstruction")
	switch existing.Type {
	case gjson.String:
		current := existing.String()
		hint := claudeInterleavedThinkingHint
		if strings.Contains(current, hint) {
			return payload
		}
		trimmed := strings.TrimSpace(current)
		if trimmed == "" {
			payload, _ = sjson.SetBytes(payload, "systemInstruction", hint)
		} else {
			payload, _ = sjson.SetBytes(payload, "systemInstruction", trimmed+"\n\n"+hint)
		}
		return payload
	case gjson.JSON:
		// Object format with parts
		parts := existing.Get("parts").Array()
		for _, p := range parts {
			if p.Get("text").String() == claudeInterleavedThinkingHint {
				return payload
			}
		}
		// Append hint as new part
		newPart := map[string]any{"text": claudeInterleavedThinkingHint}
		newPartJSON, _ := json.Marshal(newPart)
		if len(parts) == 0 {
			payload, _ = sjson.SetRawBytes(payload, "systemInstruction.parts", newPartJSON)
		} else {
			partsRaw := existing.Get("parts").Raw
			newParts := strings.TrimSuffix(partsRaw, "]") + "," + string(newPartJSON) + "]"
			payload, _ = sjson.SetRawBytes(payload, "systemInstruction", []byte("{\"parts\":"+newParts+"}"))
		}
		return payload
	default:
		// No system instruction, create one
		payload, _ = sjson.SetBytes(payload, "systemInstruction", claudeInterleavedThinkingHint)
		return payload
	}
}

// normalizeClaudeTools normalizes tools for Claude models using VALIDATED mode.
// Returns updated payload, toolDebugMissing count and debug summaries.
func normalizeClaudeTools(payload []byte) ([]byte, int, []string) {
	if !gjson.GetBytes(payload, "tools").Exists() {
		return payload, 0, nil
	}

	tools := gjson.GetBytes(payload, "tools").Array()
	if len(tools) == 0 {
		return payload, 0, nil
	}

	debugMissing := 0
	var debugSummaries []string

	var functionDeclarations []map[string]any
	var passthroughTools []map[string]any

	for _, tool := range tools {
		t := tool.String()
		var toolMap map[string]any
		if err := json.Unmarshal([]byte(t), &toolMap); err != nil {
			continue
		}

		// Check for functionDeclarations array first
		if fds, ok := toolMap["functionDeclarations"].([]interface{}); ok && len(fds) > 0 {
			for _, decl := range fds {
				declMap, ok := decl.(map[string]any)
				if !ok {
					continue
				}
				name := fmt.Sprintf("%v", declMap["name"])
				desc := fmt.Sprintf("%v", declMap["description"])
				schema := declMap["parameters"]
				if schema == nil {
					schema = map[string]any{
						"type":                 "object",
						"properties":           map[string]any{},
					}
				}
				functionDeclarations = append(functionDeclarations, map[string]any{
					"name":        sanitizeToolName(name),
					"description": desc,
					"parameters":  schema,
				})
				debugSummaries = append(debugSummaries, fmt.Sprintf("decl=%s,src=functionDeclarations", sanitizeToolName(name)))
			}
			continue
		}

		// Function/custom style declaration
		fnObj, _ := toolMap["function"].(map[string]any)
		customObj, _ := toolMap["custom"].(map[string]any)
		hasFunction := fnObj != nil
		hasCustom := customObj != nil
		hasParams := toolMap["parameters"] != nil || toolMap["input_schema"] != nil || toolMap["inputSchema"] != nil

		if hasFunction || hasCustom || hasParams {
			name := pickField([]map[string]any{fnObj, customObj, toolMap}, "name")
			desc := pickField([]map[string]any{fnObj, customObj, toolMap}, "description")
			paramSource := pickFirstNonNil([]map[string]any{fnObj, customObj, toolMap}, "parameters", "parametersJsonSchema", "input_schema", "inputSchema")
			schema := normalizeSchema(paramSource)
			if schema == nil {
				debugMissing++
				schema = placeholderSchema()
				debugSummaries = append(debugSummaries, fmt.Sprintf("decl=%s,src=function/custom,missingSchema", sanitizeToolName(name)))
			} else {
				debugSummaries = append(debugSummaries, fmt.Sprintf("decl=%s,src=function/custom,hasSchema", sanitizeToolName(name)))
			}
			functionDeclarations = append(functionDeclarations, map[string]any{
				"name":        sanitizeToolName(name),
				"description": desc,
				"parameters":  schema,
			})
			continue
		}

		// Preserve non-function tools (e.g., codeExecution, web search)
		passthroughTools = append(passthroughTools, toolMap)
	}

	// Rebuild tools array
	var finalTools []map[string]any
	if len(functionDeclarations) > 0 {
		finalTools = append(finalTools, map[string]any{"functionDeclarations": functionDeclarations})
	}
	finalTools = append(finalTools, passthroughTools...)

	payloadJSON, _ := json.Marshal(finalTools)
	payload, _ = sjson.SetRawBytes(payload, "tools", payloadJSON)
	return payload, debugMissing, debugSummaries
}

func pickString(maps ...any) string {
	for _, m := range maps {
		switch v := m.(type) {
		case map[string]any:
			if s, ok := v["name"].(string); ok && s != "" {
				return s
			}
		case string:
			// skip
		}
	}
	return ""
}

func pickField(maps []map[string]any, key string) string {
	for _, m := range maps {
		if m == nil {
			continue
		}
		if s, ok := m[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func pickFirstNonNil(maps []map[string]any, keys ...string) map[string]any {
	for _, m := range maps {
		if m == nil {
			continue
		}
		for _, k := range keys {
			if v, ok := m[k]; ok && v != nil {
				if mv, ok := v.(map[string]any); ok {
					return mv
				}
			}
		}
	}
	return nil
}

func sanitizeToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	result := b.String()
	if len(result) > 64 {
		result = result[:64]
	}
	return result
}

func placeholderSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{emptySchemaPlaceholderName: map[string]any{"type": "boolean", "description": emptySchemaPlaceholderDescription}},
		"required":   []string{emptySchemaPlaceholderName},
	}
}

func normalizeSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	if len(schema) == 0 {
		return placeholderSchema()
	}

	// Ensure type is object
	if t, ok := schema["type"].(string); !ok || strings.ToLower(t) != "object" {
		// Check if it has properties
		if _, hasProps := schema["properties"].(map[string]any); !hasProps {
			hasFields := false
			for k := range schema {
				if k != "type" && k != "description" {
					hasFields = true
					break
				}
			}
			if !hasFields {
				return placeholderSchema()
			}
		}
		schema["type"] = "object"
	}

	// Ensure properties exist and are non-empty
	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		schema["properties"] = map[string]any{emptySchemaPlaceholderName: map[string]any{
			"type":        "boolean",
			"description": emptySchemaPlaceholderDescription,
		}}
		req, _ := schema["required"].([]string)
		found := false
		for _, r := range req {
			if r == emptySchemaPlaceholderName {
				found = true
				break
			}
		}
		if !found {
			schema["required"] = append(req, emptySchemaPlaceholderName)
		}
	}

	return schema
}

// applyClaudeTransforms applies Claude-specific request transformations.
// Returns toolDebugMissing and debug summaries.
func applyClaudeTransforms(payload []byte, includeThoughts bool, thinkingBudget int) (int, []string, error) {
	// 1. Configure tool calling mode: set toolConfig.functionCallingConfig.mode = "VALIDATED"
	payload = setToolConfigValidated(payload)

	// 2. Convert stop_sequences -> stopSequences in generationConfig
	genConfig := gjson.GetBytes(payload, "generationConfig")
	if genConfig.IsObject() {
		configMap := genConfig.Value().(map[string]any)
		convertStopSequences(configMap)
		configJSON, _ := json.Marshal(configMap)
		payload, _ = sjson.SetRawBytes(payload, "generationConfig", configJSON)
	}

	// 3. Apply thinking config
	if includeThoughts && IsClaudeThinkingModel(gjson.GetBytes(payload, "model").String()) {
		thinkingConfig := buildClaudeThinkingConfig(includeThoughts, thinkingBudget)
		genConfigMap := map[string]any{}
		genConfig := gjson.GetBytes(payload, "generationConfig")
		if genConfig.IsObject() {
			genConfigMap = genConfig.Value().(map[string]any)
		}
		genConfigMap["thinkingConfig"] = thinkingConfig
		if thinkingBudget > 0 {
			ensureClaudeMaxOutputTokens(genConfigMap, thinkingBudget)
		}
		genJSON, _ := json.Marshal(genConfigMap)
		payload, _ = sjson.SetRawBytes(payload, "generationConfig", genJSON)
	}

	// 4. Append interleaved thinking hint for thinking models with tools
	if IsClaudeThinkingModel(gjson.GetBytes(payload, "model").String()) {
		if tools := gjson.GetBytes(payload, "tools"); tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
			payload = appendClaudeThinkingHint(payload)
		}
	}

	// 5. Normalize tools
	payload, debugMissing, debugSummaries := normalizeClaudeTools(payload)
	return debugMissing, debugSummaries, nil
}

func setToolConfigValidated(payload []byte) []byte {
	if !gjson.GetBytes(payload, "toolConfig").Exists() {
		payload, _ = sjson.SetBytes(payload, "toolConfig", map[string]any{})
	}
	_, _ = sjson.SetBytes(payload, "toolConfig.functionCallingConfig.mode", "VALIDATED")
	return payload
}

// toGeminiSchema transforms a JSON Schema to Gemini-compatible format.
// Key transformations: uppercase type values, remove unsupported fields, recursively process nested schemas.
var unsupportedSchemaFields = map[string]bool{
	"additionalProperties":     true,
	"$schema":                  true,
	"$id":                      true,
	"$comment":                 true,
	"$ref":                     true,
	"$defs":                    true,
	"definitions":              true,
	"const":                    true,
	"contentMediaType":         true,
	"contentEncoding":          true,
	"if":                       true,
	"then":                     true,
	"else":                     true,
	"not":                      true,
	"patternProperties":       true,
	"unevaluatedProperties":   true,
	"unevaluatedItems":        true,
	"dependentRequired":       true,
	"dependentSchemas":        true,
	"propertyNames":           true,
	"minContains":             true,
	"maxContains":             true,
}

func toGeminiSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{}
	}

	result := make(map[string]any)
	propertyNames := make(map[string]bool)
	if props, ok := schema["properties"].(map[string]any); ok {
		for k := range props {
			propertyNames[k] = true
		}
	}

	for k, v := range schema {
		if unsupportedSchemaFields[k] {
			continue
		}
		switch k {
		case "type":
			if s, ok := v.(string); ok {
				result[k] = strings.ToUpper(s)
			} else {
				result[k] = v
			}
		case "properties":
			if propMap, ok := v.(map[string]any); ok {
				newProps := make(map[string]any)
				for pn, ps := range propMap {
					newProps[pn] = toGeminiSchema(ps.(map[string]any))
				}
				result[k] = newProps
			}
		case "items":
			if itemMap, ok := v.(map[string]any); ok {
				result[k] = toGeminiSchema(itemMap)
			}
		case "anyOf", "oneOf", "allOf":
			if arr, ok := v.([]interface{}); ok {
				newArr := make([]interface{}, len(arr))
				for i, item := range arr {
					if itemMap, ok := item.(map[string]any); ok {
						newArr[i] = toGeminiSchema(itemMap)
					} else {
						newArr[i] = item
					}
				}
				result[k] = newArr
			}
		case "required":
			if arr, ok := v.([]interface{}); ok && len(propertyNames) > 0 {
				filtered := make([]interface{}, 0, len(arr))
				for _, item := range arr {
					if s, ok := item.(string); ok && propertyNames[s] {
						filtered = append(filtered, item)
					}
				}
				if len(filtered) > 0 {
					result[k] = filtered
				}
			} else {
				result[k] = v
			}
		default:
			result[k] = v
		}
	}

	// Issue #80: array types must have an items field
	if strings.EqualFold(fmt.Sprintf("%v", result["type"]), "ARRAY") && result["items"] == nil {
		result["items"] = map[string]any{"type": "STRING"}
	}

	return result
}

// buildGemini3ThinkingConfig builds thinking config for Gemini 3 models (thinkingLevel string).
func buildGemini3ThinkingConfig(includeThoughts bool, thinkingLevel string) map[string]any {
	return map[string]any{
		"includeThoughts": includeThoughts,
		"thinkingLevel":   thinkingLevel,
	}
}

// buildGemini25ThinkingConfig builds thinking config for Gemini 2.5 models (numeric thinkingBudget).
func buildGemini25ThinkingConfig(includeThoughts bool, thinkingBudget int) map[string]any {
	result := map[string]any{
		"includeThoughts": includeThoughts,
	}
	if thinkingBudget > 0 {
		result["thinkingBudget"] = thinkingBudget
	}
	return result
}

// buildImageGenerationConfig builds image generation config for Gemini image models.
// Reads OPENCODE_IMAGE_ASPECT_RATIO env var; defaults to "1:1".
func buildImageGenerationConfig() map[string]any {
	aspectRatio := "1:1"
	validRatios := []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}
	for _, r := range validRatios {
		if r == aspectRatio {
			return map[string]any{"aspectRatio": aspectRatio}
		}
	}
	return map[string]any{"aspectRatio": "1:1"}
}

// normalizeGeminiTools normalizes tools for Gemini models.
// Returns toolDebugMissing and debug summaries.
func normalizeGeminiTools(payload []byte) ([]byte, int, []string) {
	if !gjson.GetBytes(payload, "tools").Exists() {
		return payload, 0, nil
	}

	tools := gjson.GetBytes(payload, "tools").Array()
	if len(tools) == 0 {
		return payload, 0, nil
	}

	debugMissing := 0
	var debugSummaries []string

	placeholder := map[string]any{
		"type":       "OBJECT",
		"properties": map[string]any{"_placeholder": map[string]any{"type": "BOOLEAN", "description": "Placeholder. Always pass true."}},
		"required":   []string{"_placeholder"},
	}

	var newTools []map[string]any

	for idx, tool := range tools {
		t := tool.String()
		var toolMap map[string]any
		if err := json.Unmarshal([]byte(t), &toolMap); err != nil {
			continue
		}

		// Skip Google Search tools
		if toolMap["googleSearch"] != nil || toolMap["googleSearchRetrieval"] != nil {
			newTools = append(newTools, toolMap)
			continue
		}

		newTool := copyMap(toolMap)

		// Collect schema candidates
		schemaCandidates := []map[string]any{}
		if fn, ok := newTool["function"].(map[string]any); ok {
			if s, ok := fn["input_schema"].(map[string]any); ok {
				schemaCandidates = append(schemaCandidates, s)
			}
			if s, ok := fn["parameters"].(map[string]any); ok {
				schemaCandidates = append(schemaCandidates, s)
			}
			if s, ok := fn["inputSchema"].(map[string]any); ok {
				schemaCandidates = append(schemaCandidates, s)
			}
		}
		if custom, ok := newTool["custom"].(map[string]any); ok {
			if s, ok := custom["input_schema"].(map[string]any); ok {
				schemaCandidates = append(schemaCandidates, s)
			}
			if s, ok := custom["parameters"].(map[string]any); ok {
				schemaCandidates = append(schemaCandidates, s)
			}
		}
		if s, ok := newTool["parameters"].(map[string]any); ok {
			schemaCandidates = append(schemaCandidates, s)
		}
		if s, ok := newTool["input_schema"].(map[string]any); ok {
			schemaCandidates = append(schemaCandidates, s)
		}
		if s, ok := newTool["inputSchema"].(map[string]any); ok {
			schemaCandidates = append(schemaCandidates, s)
		}

		var schema map[string]any
		schemaObjectOk := false
		for _, s := range schemaCandidates {
			if s != nil && len(s) > 0 {
				schema = s
				schemaObjectOk = true
				break
			}
		}
		if !schemaObjectOk {
			schema = placeholder
			debugMissing++
		} else {
			schema = toGeminiSchema(schema)
		}

		name := pickGeminiToolName(toolMap, idx)

		// Update function.input_schema
		if fn, ok := newTool["function"].(map[string]any); ok && schema != nil {
			fn["input_schema"] = schema
		}
		// Update custom.input_schema
		if custom, ok := newTool["custom"].(map[string]any); ok && schema != nil {
			custom["input_schema"] = schema
		}
		// Create custom from function if missing
		if custom, ok := newTool["custom"].(map[string]any); !ok || custom == nil {
			if fn, ok := newTool["function"].(map[string]any); ok && fn != nil {
				newTool["custom"] = map[string]any{
					"name":        fn["name"],
					"description": fn["description"],
					"input_schema": schema,
				}
			}
		}
		// Create custom if both missing
		if _, hasCustom := newTool["custom"]; !hasCustom {
			newTool["custom"] = map[string]any{
				"name":        name,
				"description": newTool["description"],
				"input_schema": schema,
			}
			if _, hasParams := newTool["parameters"]; !hasParams {
				newTool["parameters"] = schema
			}
		}
		if custom, ok := newTool["custom"].(map[string]any); ok && custom != nil {
			if _, hasSchema := custom["input_schema"]; !hasSchema {
				custom["input_schema"] = map[string]any{
					"type":       "OBJECT",
					"properties": map[string]any{},
				}
				newTool["custom"] = custom
				debugMissing++
			}
		}

		debugSummaries = append(debugSummaries, fmt.Sprintf("idx=%d, hasCustom=%v, customSchema=%v, hasFunction=%v, functionSchema=%v",
			idx,
			newTool["custom"] != nil,
			customHasSchema(newTool["custom"]),
			newTool["function"] != nil,
			fnHasSchema(newTool["function"]),
		))

		// Strip custom wrappers for Gemini; only function-style is accepted.
		delete(newTool, "custom")

		newTools = append(newTools, newTool)
	}

	payloadJSON, _ := json.Marshal(newTools)
	payload, _ = sjson.SetRawBytes(payload, "tools", payloadJSON)
	return payload, debugMissing, debugSummaries
}

func customHasSchema(custom any) bool {
	if m, ok := custom.(map[string]any); ok {
		return m["input_schema"] != nil
	}
	return false
}

func fnHasSchema(fn any) bool {
	if m, ok := fn.(map[string]any); ok {
		return m["input_schema"] != nil || m["parameters"] != nil || m["inputSchema"] != nil
	}
	return false
}

func pickGeminiToolName(toolMap map[string]any, idx int) string {
	if v, ok := toolMap["name"].(string); ok && v != "" {
		return v
	}
	if fn, ok := toolMap["function"].(map[string]any); ok {
		if v, ok := fn["name"].(string); ok && v != "" {
			return v
		}
	}
	if custom, ok := toolMap["custom"].(map[string]any); ok {
		if v, ok := custom["name"].(string); ok && v != "" {
			return v
		}
	}
	return fmt.Sprintf("tool-%d", idx)
}

func copyMap(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// wrapToolsAsFunctionDeclarations wraps tools in Gemini's required functionDeclarations format.
func wrapToolsAsFunctionDeclarations(payload []byte) (int, int, error) {
	if !gjson.GetBytes(payload, "tools").Exists() {
		return 0, 0, nil
	}

	tools := gjson.GetBytes(payload, "tools").Array()
	if len(tools) == 0 {
		return 0, 0, nil
	}

	var functionDeclarations []map[string]any
	var passthroughTools []map[string]any
	hasWebSearch := false

	for _, tool := range tools {
		t := tool.String()
		var toolMap map[string]any
		if err := json.Unmarshal([]byte(t), &toolMap); err != nil {
			continue
		}

		// Passthrough: googleSearch, googleSearchRetrieval, codeExecution
		if toolMap["googleSearch"] != nil || toolMap["googleSearchRetrieval"] != nil || toolMap["codeExecution"] != nil {
			passthroughTools = append(passthroughTools, toolMap)
			continue
		}

		// Web search detection
		if isWebSearchTool(toolMap) {
			hasWebSearch = true
			continue
		}

		// functionDeclarations format
		if fds, ok := toolMap["functionDeclarations"].([]interface{}); ok && len(fds) > 0 {
			for _, decl := range fds {
				declMap, ok := decl.(map[string]any)
				if !ok {
					continue
				}
				params, _ := declMap["parameters"].(map[string]any)
				if params == nil {
					params = map[string]any{"type": "OBJECT", "properties": map[string]any{}}
				}
				functionDeclarations = append(functionDeclarations, map[string]any{
					"name":        fmt.Sprintf("%v", declMap["name"]),
					"description": fmt.Sprintf("%v", declMap["description"]),
					"parameters":  params,
				})
			}
			continue
		}

		// Single function/custom format
		fn, _ := toolMap["function"].(map[string]any)
		custom, _ := toolMap["custom"].(map[string]any)
		name := pickGeminiToolName(toolMap, len(functionDeclarations))
		desc := ""
		if fn != nil {
			desc = fmt.Sprintf("%v", fn["description"])
		} else if custom != nil {
			desc = fmt.Sprintf("%v", custom["description"])
		} else {
			desc = fmt.Sprintf("%v", toolMap["description"])
		}
		schema := firstGeminiSchema(fn, custom, toolMap)
		functionDeclarations = append(functionDeclarations, map[string]any{
			"name":        name,
			"description": desc,
			"parameters":  schema,
		})
	}

	var finalTools []map[string]any
	if len(functionDeclarations) > 0 {
		finalTools = append(finalTools, map[string]any{"functionDeclarations": functionDeclarations})
	}
	finalTools = append(finalTools, passthroughTools...)
	if hasWebSearch && len(functionDeclarations) == 0 {
		finalTools = append(finalTools, map[string]any{"googleSearch": map[string]any{}})
	}

	payloadJSON, _ := json.Marshal(finalTools)
	payload, _ = sjson.SetRawBytes(payload, "tools", payloadJSON)
	wrappedCount := len(functionDeclarations)
	passthroughCount := len(passthroughTools)
	if hasWebSearch && len(functionDeclarations) == 0 {
		passthroughCount++
	}
	return wrappedCount, passthroughCount, nil
}

func firstGeminiSchema(fn, custom map[string]any, toolMap map[string]any) map[string]any {
	candidates := []map[string]any{}
	if fn != nil {
		if s, ok := fn["input_schema"].(map[string]any); ok {
			candidates = append(candidates, s)
		}
		if s, ok := fn["parameters"].(map[string]any); ok {
			candidates = append(candidates, s)
		}
		if s, ok := fn["inputSchema"].(map[string]any); ok {
			candidates = append(candidates, s)
		}
	}
	if custom != nil {
		if s, ok := custom["input_schema"].(map[string]any); ok {
			candidates = append(candidates, s)
		}
		if s, ok := custom["parameters"].(map[string]any); ok {
			candidates = append(candidates, s)
		}
	}
	if s, ok := toolMap["parameters"].(map[string]any); ok {
		candidates = append(candidates, s)
	}
	if s, ok := toolMap["input_schema"].(map[string]any); ok {
		candidates = append(candidates, s)
	}
	if s, ok := toolMap["inputSchema"].(map[string]any); ok {
		candidates = append(candidates, s)
	}
	for _, s := range candidates {
		if s != nil && len(s) > 0 {
			return s
		}
	}
	return map[string]any{"type": "OBJECT", "properties": map[string]any{}}
}

func isWebSearchTool(tool map[string]any) bool {
	if tool["googleSearch"] != nil || tool["googleSearchRetrieval"] != nil {
		return true
	}
	if tool["type"] == "web_search_20250305" {
		return true
	}
	if name, ok := tool["name"].(string); ok {
		if name == "web_search" || name == "google_search" {
			return true
		}
	}
	return false
}

// applyGeminiTransforms applies Gemini-specific request transformations.
// Returns toolDebugMissing, debug summaries, wrappedFunctionCount, passthroughToolCount, error.
func applyGeminiTransforms(payload []byte, model string, tierThinkingBudget int, tierThinkingLevel string, googleSearchMode string) (int, []string, int, int, error) {
	// 1. Apply thinking config
	lowerModel := strings.ToLower(model)
	if IsGemini3Model(lowerModel) && tierThinkingLevel != "" {
		thinkingConfig := buildGemini3ThinkingConfig(true, tierThinkingLevel)
		genConfigMap := map[string]any{}
		if gjson.GetBytes(payload, "generationConfig").IsObject() {
			genConfigMap = gjson.GetBytes(payload, "generationConfig").Value().(map[string]any)
		}
		genConfigMap["thinkingConfig"] = thinkingConfig
		genJSON, _ := json.Marshal(genConfigMap)
		payload, _ = sjson.SetRawBytes(payload, "generationConfig", genJSON)
	} else if IsGemini25Model(lowerModel) || IsGemini3Model(lowerModel) {
		budget := tierThinkingBudget
		if budget <= 0 {
			budget = 0 // default: no numeric budget for Gemini 3
		}
		thinkingConfig := buildGemini25ThinkingConfig(true, budget)
		genConfigMap := map[string]any{}
		if gjson.GetBytes(payload, "generationConfig").IsObject() {
			genConfigMap = gjson.GetBytes(payload, "generationConfig").Value().(map[string]any)
		}
		genConfigMap["thinkingConfig"] = thinkingConfig
		genJSON, _ := json.Marshal(genConfigMap)
		payload, _ = sjson.SetRawBytes(payload, "generationConfig", genJSON)
	}

	// 2. Apply Google Search if enabled
	if googleSearchMode == "auto" {
		tools := gjson.GetBytes(payload, "tools")
		if !tools.Exists() {
			payload, _ = sjson.SetBytes(payload, "tools", []map[string]any{})
		}
		toolsArr, _ := json.Marshal([]map[string]any{{"googleSearch": map[string]any{}}})
		payload, _ = sjson.SetRawBytes(payload, "tools", toolsArr)
	}

	// 3. Normalize tools
	payload, debugMissing, debugSummaries := normalizeGeminiTools(payload)

	// 4. Wrap tools in functionDeclarations format
	wrappedCount, passthroughCount, err := wrapToolsAsFunctionDeclarations(payload)
	if err != nil {
		return debugMissing, debugSummaries, 0, 0, err
	}

	return debugMissing, debugSummaries, wrappedCount, passthroughCount, nil
}

// ResolveGeminiThinkingConfig resolves thinking config for Gemini models based on model tier.
func ResolveGeminiThinkingConfig(model, tierThinkingLevel string, tierThinkingBudget int, includeThoughts bool) map[string]any {
	lower := strings.ToLower(model)
	if IsGemini3Model(lower) && tierThinkingLevel != "" {
		return buildGemini3ThinkingConfig(includeThoughts, tierThinkingLevel)
	}
	budget := tierThinkingBudget
	if budget <= 0 {
		budget = 0
	}
	return buildGemini25ThinkingConfig(includeThoughts, budget)
}

// MapGeminiModelToAntigravityModel maps Gemini CLI models to antigravity backends.
func MapGeminiModelToAntigravityModel(model, tier string) string {
	lower := strings.ToLower(model)
	// Direct antigravity model passthrough
	if strings.HasPrefix(lower, "antigravity-") {
		return model
	}
	// Gemini CLI models: map based on tier
	if IsGeminiModel(model) {
		return GetGemini35FlashAntigravityModel(tier)
	}
	return model
}

// GradientMapSize is the max map size for gradient calculations.
const gradientMapSize = 256

// generateGradientMap creates a color gradient map for UI rendering (placeholder for now).
func generateGradientMap(colors []string) []string {
	if len(colors) == 0 {
		return nil
	}
	return colors
}

// defaultThicknessMultiplier is a placeholder metric value.
const defaultThicknessMultiplier = 1.0

// clampFloat64 clamps a float64 value between min and max.
func clampFloat64(v, minVal, maxVal float64) float64 {
	if v < minVal {
		return minVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

// fractionToPercentage converts a 0-1 fraction to a percentage string.
func fractionToPercentage(fraction float64) string {
	pct := clampFloat64(fraction*100, 0, 100)
	return fmt.Sprintf("%.0f%%", math.Floor(pct+0.5))
}

// clampModelBudget clamps a thinking budget to valid range [0, maxOutputTokens).
func clampModelBudget(budget, maxOutput int) int {
	if budget < 0 {
		return 0
	}
	if maxOutput > 0 && budget >= maxOutput {
		return maxOutput - 1
	}
	return budget
}

// thinkingLevelFromBudget derives a thinking tier (minimal/low/medium/high) from a numeric budget.
func thinkingLevelFromBudget(budget int) string {
	switch {
	case budget <= 0:
		return thinkingLevelNone
	case budget <= 2048:
		return "minimal"
	case budget <= 8192:
		return "low"
	case budget <= 32768:
		return "medium"
	default:
		return "high"
	}
}

// ThinkingTier is a normalized thinking tier suffix from model names.
type ThinkingTier string

const (
	ThinkingTierMinimal ThinkingTier = "minimal"
	ThinkingTierLow     ThinkingTier = "low"
	ThinkingTierMedium  ThinkingTier = "medium"
	ThinkingTierHigh    ThinkingTier = "high"
)

// ModelFamily identifies the broad target family for transforms and sanitization.
type ModelFamily string

const (
	ModelFamilyClaude      ModelFamily = "claude"
	ModelFamilyGeminiFlash ModelFamily = "gemini-flash"
	ModelFamilyGeminiPro   ModelFamily = "gemini-pro"
	ModelFamilyUnknown     ModelFamily = "unknown"
)

// RequestPayload is the generic JSON payload shape used by transform helpers.
type RequestPayload map[string]any

// ThinkingConfig is the backend thinking configuration object.
type ThinkingConfig map[string]any

// NormalizedThinkingConfig carries normalized user thinking settings.
type NormalizedThinkingConfig struct {
	IncludeThoughts *bool
	ThinkingBudget  int
}

// GoogleSearchConfig carries Gemini grounding settings.
type GoogleSearchConfig struct {
	Mode      string
	Threshold float64
}

// ClaudeTransformOptions configures ApplyClaudeTransforms.
type ClaudeTransformOptions struct {
	Model              string
	TierThinkingBudget int
	NormalizedThinking *NormalizedThinkingConfig
	CleanJSONSchema    func(schema any) map[string]any
}

// ClaudeTransformResult is returned by ApplyClaudeTransforms.
type ClaudeTransformResult struct {
	Payload            []byte
	ToolDebugMissing   int
	ToolDebugSummaries []string
}

// GeminiTransformOptions configures ApplyGeminiTransforms.
type GeminiTransformOptions struct {
	Model              string
	TierThinkingBudget int
	TierThinkingLevel  ThinkingTier
	NormalizedThinking *NormalizedThinkingConfig
	GoogleSearch       *GoogleSearchConfig
}

// GeminiTransformResult is returned by ApplyGeminiTransforms.
type GeminiTransformResult struct {
	Payload              []byte
	ToolDebugMissing     int
	ToolDebugSummaries   []string
	WrappedFunctionCount int
	PassthroughToolCount int
}

// ImageConfig is the Gemini image-generation configuration.
type ImageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
}

// TransformContext contains request transformation context.
type TransformContext struct {
	ProjectID       string
	Model           string
	RequestedModel  string
	Family          ModelFamily
	Streaming       bool
	RequestID       string
	SessionID       string
	ThinkingTier    ThinkingTier
	ThinkingBudget  int
	ThinkingLevel   string
}

// TransformDebugInfo describes a transform pass.
type TransformDebugInfo struct {
	Transformer      string
	ToolCount        int
	ToolsTransformed bool
	ThinkingTier     string
	ThinkingBudget   int
	ThinkingLevel    string
}

// TransformResult is a transformed JSON body plus debug info.
type TransformResult struct {
	Body      string
	DebugInfo TransformDebugInfo
}

// ResolvedModel is a resolved model name and tier/quota configuration.
type ResolvedModel struct {
	ActualModel     string
	ThinkingLevel   string
	ThinkingBudget  int
	Tier            ThinkingTier
	IsThinkingModel bool
	IsImageModel    bool
	QuotaPreference HeaderStyle
	ExplicitQuota   bool
	ConfigSource    string
	GoogleSearch    *GoogleSearchConfig
}

// ModelResolverOptions configures ResolveModelWithTier.
type ModelResolverOptions struct {
	CLIFirst bool
}

// SanitizerOptions configures SanitizeCrossModelPayload.
type SanitizerOptions struct {
	TargetModel                  string
	SourceModel                  string
	PreserveNonSignatureMetadata *bool
}

// SanitizationResult is returned by SanitizeCrossModelPayload.
type SanitizationResult struct {
	Payload             []byte
	Modified            bool
	SignaturesStripped  int
}

// WrapToolsResult describes Gemini tool wrapping.
type WrapToolsResult struct {
	Payload              []byte
	WrappedFunctionCount int
	PassthroughToolCount int
}

// ConfigureClaudeToolConfig sets Claude tool calling to VALIDATED mode.
func ConfigureClaudeToolConfig(payload []byte) ([]byte, error) {
	updated, err := sjson.SetBytes(payload, "toolConfig.functionCallingConfig.mode", "VALIDATED")
	if err != nil {
		return payload, err
	}
	return updated, nil
}

// BuildClaudeThinkingConfig builds Claude thinking config with camelCase keys
// (matching headerStyle=antigravity in the cortexkit reference client).
func BuildClaudeThinkingConfig(includeThoughts bool, thinkingBudget int) ThinkingConfig {
	return ThinkingConfig(buildClaudeThinkingConfig(includeThoughts, thinkingBudget))
}

// EnsureClaudeMaxOutputTokens is the exported wrapper for the antigravity-aware
// ensureClaudeMaxOutputTokens (pins 64000 when no client max is set).
func EnsureClaudeMaxOutputTokens(generationConfig map[string]any, thinkingBudget int) {
	if generationConfig == nil {
		return
	}
	ensureClaudeMaxOutputTokens(generationConfig, thinkingBudget)
}

// ConvertStopSequences converts generationConfig.stop_sequences to stopSequences.
func ConvertStopSequences(generationConfig map[string]any) {
	if generationConfig == nil {
		return
	}
	if seqs, ok := generationConfig["stop_sequences"]; ok {
		switch seqs.(type) {
		case []any, []string:
			generationConfig["stopSequences"] = seqs
			delete(generationConfig, "stop_sequences")
		}
	}
}

// dropUnsignedThoughtParts removes Gemini-style thought parts (thought: true) without a
// thoughtSignature from the contents array. Antigravity relays these to Claude as thinking
// blocks; Claude requires a signature on every thinking block so unsigned ones must be dropped.
func dropUnsignedThoughtParts(payload []byte) []byte {
	contents := gjson.GetBytes(payload, "contents")
	if !contents.IsArray() {
		return payload
	}
	modified := false
	newContents := make([]string, 0, len(contents.Array()))
	for _, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			newContents = append(newContents, content.Raw)
			continue
		}
		kept := make([]string, 0, len(parts.Array()))
		anyDropped := false
		for _, part := range parts.Array() {
			if part.Get("thought").Bool() {
				sig := strings.TrimSpace(part.Get("thoughtSignature").String())
				if sig == "" {
					anyDropped = true
					continue // drop unsigned thought part
				}
			}
			kept = append(kept, part.Raw)
		}
		if anyDropped {
			modified = true
			updated, _ := sjson.SetRawBytes([]byte(content.Raw), "parts", []byte("["+strings.Join(kept, ",")+"]"))
			newContents = append(newContents, string(updated))
		} else {
			newContents = append(newContents, content.Raw)
		}
	}
	if !modified {
		return payload
	}
	result, _ := sjson.SetRawBytes(payload, "contents", []byte("["+strings.Join(newContents, ",")+"]"))
	return result
}

// dropUnsignedClaudeThinkingMessages removes Anthropic-style thinking content blocks
// (type: "thinking") without a signature from messages array.
func dropUnsignedClaudeThinkingMessages(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	modified := false
	newMessages := make([]string, 0, len(messages.Array()))
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			newMessages = append(newMessages, msg.Raw)
			continue
		}
		kept := make([]string, 0, len(content.Array()))
		anyDropped := false
		for _, part := range content.Array() {
			if part.Get("type").String() == "thinking" {
				sig := strings.TrimSpace(part.Get("signature").String())
				if sig == "" {
					anyDropped = true
					continue // drop unsigned thinking block
				}
			}
			kept = append(kept, part.Raw)
		}
		if anyDropped {
			modified = true
			updated, _ := sjson.SetRawBytes([]byte(msg.Raw), "content", []byte("["+strings.Join(kept, ",")+"]"))
			newMessages = append(newMessages, string(updated))
		} else {
			newMessages = append(newMessages, msg.Raw)
		}
	}
	if !modified {
		return payload
	}
	result, _ := sjson.SetRawBytes(payload, "messages", []byte("["+strings.Join(newMessages, ",")+"]"))
	return result
}

// ApplyClaudeTransforms applies all Claude-specific JSON transforms.
func ApplyClaudeTransforms(payload []byte, options ClaudeTransformOptions) (ClaudeTransformResult, error) {
	var err error
	payload, err = ConfigureClaudeToolConfig(payload)
	if err != nil {
		return ClaudeTransformResult{Payload: payload}, err
	}

	// Drop thought parts and thinking blocks without signatures before sending to
	// Antigravity → Claude. Claude requires a valid signature on every thinking block
	// in conversation history; unsigned blocks cause 400 INVALID_ARGUMENT errors.
	payload = dropUnsignedThoughtParts(payload)
	payload = dropUnsignedClaudeThinkingMessages(payload)

	// Rename snake_case system_instruction → systemInstruction (camelCase).
	if gjson.GetBytes(payload, "system_instruction").Exists() && !gjson.GetBytes(payload, "systemInstruction").Exists() {
		raw := gjson.GetBytes(payload, "system_instruction").Raw
		payload, _ = sjson.SetRawBytes(payload, "systemInstruction", []byte(raw))
		payload, _ = sjson.DeleteBytes(payload, "system_instruction")
	}

	// Convert bare "system" field → systemInstruction.
	// Antigravity API does not accept a "system" field; it expects systemInstruction.
	if sys := gjson.GetBytes(payload, "system"); sys.Exists() && !gjson.GetBytes(payload, "systemInstruction").Exists() {
		switch sys.Type {
		case gjson.String:
			text := sys.String()
			if text != "" {
				si := map[string]any{"parts": []map[string]any{{"text": text}}}
				siJSON, _ := json.Marshal(si)
				payload, _ = sjson.SetRawBytes(payload, "systemInstruction", siJSON)
			}
		case gjson.JSON:
			payload, _ = sjson.SetRawBytes(payload, "systemInstruction", []byte(sys.Raw))
		}
		payload, _ = sjson.DeleteBytes(payload, "system")
	} else if sys.Exists() {
		// systemInstruction already present; just drop the conflicting "system" field.
		payload, _ = sjson.DeleteBytes(payload, "system")
	}

	// Normalize snake_case cached_content → cachedContent (camelCase).
	if gjson.GetBytes(payload, "cached_content").Exists() && !gjson.GetBytes(payload, "cachedContent").Exists() {
		raw := gjson.GetBytes(payload, "cached_content").Raw
		payload, _ = sjson.SetRawBytes(payload, "cachedContent", []byte(raw))
		payload, _ = sjson.DeleteBytes(payload, "cached_content")
	}

	// Clean up extra_body thinking fields if present.
	if gjson.GetBytes(payload, "extra_body").IsObject() {
		payload, _ = sjson.DeleteBytes(payload, "extra_body.thinkingConfig")
		payload, _ = sjson.DeleteBytes(payload, "extra_body.thinking")
	}

	// Delete top-level thinkingConfig and thinking fields.
	// These are set inside generationConfig.thinkingConfig instead.
	payload, _ = sjson.DeleteBytes(payload, "thinkingConfig")
	payload, _ = sjson.DeleteBytes(payload, "thinking")

	if gjson.GetBytes(payload, "generationConfig").IsObject() {
		generationConfig := objectAt(payload, "generationConfig")
		ConvertStopSequences(generationConfig)
		payload, err = setObjectAt(payload, "generationConfig", generationConfig)
		if err != nil {
			return ClaudeTransformResult{Payload: payload}, err
		}
	}

	isThinking := IsClaudeThinkingModel(options.Model)
	if options.NormalizedThinking != nil && isThinking {
		thinkingBudget := options.NormalizedThinking.ThinkingBudget
		if options.TierThinkingBudget > 0 {
			thinkingBudget = options.TierThinkingBudget
		}
		includeThoughts := true
		if options.NormalizedThinking.IncludeThoughts != nil {
			includeThoughts = *options.NormalizedThinking.IncludeThoughts
		}

		generationConfig := objectAt(payload, "generationConfig")
		generationConfig["thinkingConfig"] = BuildClaudeThinkingConfig(includeThoughts, thinkingBudget)
		EnsureClaudeMaxOutputTokens(generationConfig, thinkingBudget)
		payload, err = setObjectAt(payload, "generationConfig", generationConfig)
		if err != nil {
			return ClaudeTransformResult{Payload: payload}, err
		}
	}

	if isThinking {
		tools := gjson.GetBytes(payload, "tools")
		if tools.IsArray() && len(tools.Array()) > 0 {
			payload = appendClaudeThinkingHint(payload)
		}
	}

	payload, missing, summaries := normalizeClaudeTools(payload)
	return ClaudeTransformResult{Payload: payload, ToolDebugMissing: missing, ToolDebugSummaries: summaries}, nil
}

// BuildGemini3ThinkingConfig builds Gemini 3 thinking config using a thinkingLevel string.
func BuildGemini3ThinkingConfig(includeThoughts bool, thinkingLevel ThinkingTier) ThinkingConfig {
	return ThinkingConfig(buildGemini3ThinkingConfig(includeThoughts, string(thinkingLevel)))
}

// BuildGemini25ThinkingConfig builds Gemini 2.5 thinking config using a numeric budget.
func BuildGemini25ThinkingConfig(includeThoughts bool, thinkingBudget int) ThinkingConfig {
	return ThinkingConfig(buildGemini25ThinkingConfig(includeThoughts, thinkingBudget))
}

// BuildImageGenerationConfig builds Gemini image-generation config from OPENCODE_IMAGE_ASPECT_RATIO.
func BuildImageGenerationConfig() ImageConfig {
	aspectRatio := os.Getenv("OPENCODE_IMAGE_ASPECT_RATIO")
	if aspectRatio == "" {
		aspectRatio = "1:1"
	}
	validRatios := map[string]bool{
		"1:1": true, "2:3": true, "3:2": true, "3:4": true, "4:3": true,
		"4:5": true, "5:4": true, "9:16": true, "16:9": true, "21:9": true,
	}
	if !validRatios[aspectRatio] {
		aspectRatio = "1:1"
	}
	return ImageConfig{AspectRatio: aspectRatio}
}

// NormalizeGeminiTools normalizes Gemini tools and returns the updated payload.
func NormalizeGeminiTools(payload []byte) ([]byte, int, []string) {
	return normalizeGeminiTools(payload)
}

// WrapToolsAsFunctionDeclarations wraps Gemini tools in functionDeclarations format.
func WrapToolsAsFunctionDeclarations(payload []byte) (WrapToolsResult, error) {
	if !gjson.GetBytes(payload, "tools").Exists() {
		return WrapToolsResult{Payload: payload}, nil
	}
	tools := gjson.GetBytes(payload, "tools").Array()
	if len(tools) == 0 {
		return WrapToolsResult{Payload: payload}, nil
	}

	var functionDeclarations []map[string]any
	var passthroughTools []map[string]any
	hasWebSearch := false

	for _, tool := range tools {
		var toolMap map[string]any
		if err := json.Unmarshal([]byte(tool.Raw), &toolMap); err != nil {
			continue
		}
		if toolMap["googleSearch"] != nil || toolMap["googleSearchRetrieval"] != nil || toolMap["codeExecution"] != nil {
			passthroughTools = append(passthroughTools, toolMap)
			continue
		}
		if isWebSearchTool(toolMap) {
			hasWebSearch = true
			continue
		}
		if fds, ok := toolMap["functionDeclarations"].([]interface{}); ok && len(fds) > 0 {
			for _, decl := range fds {
				declMap, ok := decl.(map[string]any)
				if !ok {
					continue
				}
				params, _ := declMap["parameters"].(map[string]any)
				if params == nil {
					params = map[string]any{"type": "OBJECT", "properties": map[string]any{}}
				}
				functionDeclarations = append(functionDeclarations, map[string]any{
					"name":        stringOrDefault(declMap["name"], fmt.Sprintf("tool-%d", len(functionDeclarations))),
					"description": stringOrDefault(declMap["description"], ""),
					"parameters":  params,
				})
			}
			continue
		}
		fn, _ := toolMap["function"].(map[string]any)
		custom, _ := toolMap["custom"].(map[string]any)
		name := stringOrDefault(firstNonEmpty(toolMap["name"], mapValue(fn, "name"), mapValue(custom, "name")), fmt.Sprintf("tool-%d", len(functionDeclarations)))
		desc := stringOrDefault(firstNonEmpty(toolMap["description"], mapValue(fn, "description"), mapValue(custom, "description")), "")
		functionDeclarations = append(functionDeclarations, map[string]any{
			"name":        name,
			"description": desc,
			"parameters":  firstGeminiSchema(fn, custom, toolMap),
		})
	}

	var finalTools []map[string]any
	if len(functionDeclarations) > 0 {
		finalTools = append(finalTools, map[string]any{"functionDeclarations": functionDeclarations})
	}
	finalTools = append(finalTools, passthroughTools...)
	if hasWebSearch && len(functionDeclarations) == 0 {
		finalTools = append(finalTools, map[string]any{"googleSearch": map[string]any{}})
	}
	encoded, err := json.Marshal(finalTools)
	if err != nil {
		return WrapToolsResult{Payload: payload}, err
	}
	payload, err = sjson.SetRawBytes(payload, "tools", encoded)
	if err != nil {
		return WrapToolsResult{Payload: payload}, err
	}
	passthroughCount := len(passthroughTools)
	if hasWebSearch && len(functionDeclarations) == 0 {
		passthroughCount++
	}
	return WrapToolsResult{Payload: payload, WrappedFunctionCount: len(functionDeclarations), PassthroughToolCount: passthroughCount}, nil
}

// ApplyGeminiTransforms applies all Gemini-specific JSON transforms.
func ApplyGeminiTransforms(payload []byte, options GeminiTransformOptions) (GeminiTransformResult, error) {
	var err error
	if options.NormalizedThinking != nil {
		includeThoughts := true
		if options.NormalizedThinking.IncludeThoughts != nil {
			includeThoughts = *options.NormalizedThinking.IncludeThoughts
		}
		var thinkingConfig ThinkingConfig
		if options.TierThinkingLevel != "" && IsGemini3Model(options.Model) {
			thinkingConfig = BuildGemini3ThinkingConfig(includeThoughts, options.TierThinkingLevel)
		} else {
			thinkingBudget := options.NormalizedThinking.ThinkingBudget
			if options.TierThinkingBudget > 0 {
				thinkingBudget = options.TierThinkingBudget
			}
			thinkingConfig = BuildGemini25ThinkingConfig(includeThoughts, thinkingBudget)
		}
		generationConfig := objectAt(payload, "generationConfig")
		generationConfig["thinkingConfig"] = thinkingConfig
		payload, err = setObjectAt(payload, "generationConfig", generationConfig)
		if err != nil {
			return GeminiTransformResult{Payload: payload}, err
		}
	}

	if options.GoogleSearch != nil && options.GoogleSearch.Mode == "auto" {
		tools := gjson.GetBytes(payload, "tools")
		var toolsArray []any
		if tools.IsArray() {
			_ = json.Unmarshal([]byte(tools.Raw), &toolsArray)
		}
		toolsArray = append(toolsArray, map[string]any{"googleSearch": map[string]any{}})
		encoded, _ := json.Marshal(toolsArray)
		payload, err = sjson.SetRawBytes(payload, "tools", encoded)
		if err != nil {
			return GeminiTransformResult{Payload: payload}, err
		}
	}

	payload, missing, summaries := NormalizeGeminiTools(payload)
	wrapResult, err := WrapToolsAsFunctionDeclarations(payload)
	if err != nil {
		return GeminiTransformResult{Payload: payload, ToolDebugMissing: missing, ToolDebugSummaries: summaries}, err
	}
	return GeminiTransformResult{
		Payload:              wrapResult.Payload,
		ToolDebugMissing:     missing,
		ToolDebugSummaries:   summaries,
		WrappedFunctionCount: wrapResult.WrappedFunctionCount,
		PassthroughToolCount: wrapResult.PassthroughToolCount,
	}, nil
}

func objectAt(payload []byte, path string) map[string]any {
	result := map[string]any{}
	value := gjson.GetBytes(payload, path)
	if value.IsObject() {
		_ = json.Unmarshal([]byte(value.Raw), &result)
	}
	return result
}

func setObjectAt(payload []byte, path string, value map[string]any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload, err
	}
	return sjson.SetRawBytes(payload, path, encoded)
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

func stringOrDefault(value any, fallback string) string {
	if value == nil {
		return fallback
	}
	s := fmt.Sprintf("%v", value)
	if s == "" || s == "<nil>" {
		return fallback
	}
	return s
}

func mapValue(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

func firstNonEmpty(values ...any) any {
	for _, value := range values {
		if s := stringOrDefault(value, ""); s != "" {
			return value
		}
	}
	return nil
}

var thinkingTierBudgets = map[string]map[ThinkingTier]int{
	"claude":           {ThinkingTierLow: 8192, ThinkingTierMedium: 16384, ThinkingTierHigh: 32768},
	"gemini-2.5-pro":   {ThinkingTierLow: 8192, ThinkingTierMedium: 16384, ThinkingTierHigh: 32768},
	"gemini-2.5-flash": {ThinkingTierLow: 6144, ThinkingTierMedium: 12288, ThinkingTierHigh: 24576},
	"default":          {ThinkingTierLow: 4096, ThinkingTierMedium: 8192, ThinkingTierHigh: 16384},
}

// GetTransformModelFamily returns a broad model family for routing decisions.
func GetTransformModelFamily(model string) ModelFamily {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "claude") {
		return ModelFamilyClaude
	}
	if strings.Contains(lower, "flash") {
		return ModelFamilyGeminiFlash
	}
	if strings.Contains(lower, "gemini") {
		return ModelFamilyGeminiPro
	}
	return ModelFamilyUnknown
}

// GetSanitizerModelFamily returns claude/gemini/unknown for cross-model metadata sanitization.
func GetSanitizerModelFamily(model string) ModelFamily {
	if IsClaudeModel(model) {
		return ModelFamilyClaude
	}
	if IsGeminiModel(model) {
		if strings.Contains(strings.ToLower(model), "flash") {
			return ModelFamilyGeminiFlash
		}
		return ModelFamilyGeminiPro
	}
	return ModelFamilyUnknown
}

// StripGeminiThinkingMetadata removes Gemini thought signatures from a content part.
func StripGeminiThinkingMetadata(part map[string]any, preserveNonSignature bool) (map[string]any, int) {
	stripped := 0
	if _, ok := part["thoughtSignature"]; ok {
		delete(part, "thoughtSignature")
		stripped++
	}
	if _, ok := part["thinkingMetadata"]; ok {
		delete(part, "thinkingMetadata")
		stripped++
	}
	metadata, ok := part["metadata"].(map[string]any)
	if ok {
		google, ok := metadata["google"].(map[string]any)
		if ok {
			for _, field := range []string{"thoughtSignature", "thinkingMetadata"} {
				if _, exists := google[field]; exists {
					delete(google, field)
					stripped++
				}
			}
			if !preserveNonSignature || len(google) == 0 {
				delete(metadata, "google")
			}
			if len(metadata) == 0 {
				delete(part, "metadata")
			}
		}
	}
	return part, stripped
}

// StripClaudeThinkingFields removes Claude thinking signatures from a content part.
func StripClaudeThinkingFields(part map[string]any) (map[string]any, int) {
	stripped := 0
	partType, _ := part["type"].(string)
	if partType == "thinking" || partType == "redacted_thinking" {
		if _, ok := part["signature"]; ok {
			delete(part, "signature")
			stripped++
		}
	}
	if signature, ok := part["signature"].(string); ok && len(signature) >= 50 {
		delete(part, "signature")
		stripped++
	}
	return part, stripped
}

// DeepSanitizeCrossModelMetadata strips foreign model signatures from nested payload content.
func DeepSanitizeCrossModelMetadata(obj any, targetFamily ModelFamily, preserveNonSignature bool) (any, int) {
	root, ok := obj.(map[string]any)
	if !ok {
		return obj, 0
	}
	result := copyMap(root)
	total := 0
	if contents, ok := result["contents"].([]any); ok {
		result["contents"], total = sanitizeContentList(contents, targetFamily, preserveNonSignature, total)
	}
	if messages, ok := result["messages"].([]any); ok {
		result["messages"], total = sanitizeMessageList(messages, targetFamily, preserveNonSignature, total)
	}
	if extraBody, ok := result["extra_body"].(map[string]any); ok {
		extraCopy := copyMap(extraBody)
		if messages, ok := extraCopy["messages"].([]any); ok {
			extraCopy["messages"], total = sanitizeMessageList(messages, targetFamily, preserveNonSignature, total)
		}
		result["extra_body"] = extraCopy
	}
	if requests, ok := result["requests"].([]any); ok {
		sanitizedRequests := make([]any, 0, len(requests))
		for _, req := range requests {
			sanitized, stripped := DeepSanitizeCrossModelMetadata(req, targetFamily, preserveNonSignature)
			total += stripped
			sanitizedRequests = append(sanitizedRequests, sanitized)
		}
		result["requests"] = sanitizedRequests
	}
	return result, total
}

// SanitizeCrossModelPayload strips model-family-specific thought signatures from JSON payload bytes.
func SanitizeCrossModelPayload(payload []byte, options SanitizerOptions) (SanitizationResult, error) {
	targetFamily := GetSanitizerModelFamily(options.TargetModel)
	if targetFamily == ModelFamilyUnknown {
		return SanitizationResult{Payload: payload}, nil
	}
	preserve := true
	if options.PreserveNonSignatureMetadata != nil {
		preserve = *options.PreserveNonSignatureMetadata
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return SanitizationResult{Payload: payload}, err
	}
	sanitized, stripped := DeepSanitizeCrossModelMetadata(root, targetFamily, preserve)
	if stripped == 0 {
		return SanitizationResult{Payload: payload}, nil
	}
	encoded, err := json.Marshal(sanitized)
	if err != nil {
		return SanitizationResult{Payload: payload}, err
	}
	return SanitizationResult{Payload: encoded, Modified: true, SignaturesStripped: stripped}, nil
}

// SanitizeCrossModelPayloadInPlace strips signatures from an already-decoded payload map.
func SanitizeCrossModelPayloadInPlace(payload map[string]any, options SanitizerOptions) int {
	targetFamily := GetSanitizerModelFamily(options.TargetModel)
	if targetFamily == ModelFamilyUnknown {
		return 0
	}
	preserve := true
	if options.PreserveNonSignatureMetadata != nil {
		preserve = *options.PreserveNonSignatureMetadata
	}
	sanitized, stripped := DeepSanitizeCrossModelMetadata(payload, targetFamily, preserve)
	if sanitizedMap, ok := sanitized.(map[string]any); ok {
		for key := range payload {
			delete(payload, key)
		}
		for key, value := range sanitizedMap {
			payload[key] = value
		}
	}
	return stripped
}

func sanitizeContentList(contents []any, targetFamily ModelFamily, preserveNonSignature bool, runningTotal int) ([]any, int) {
	sanitized := make([]any, 0, len(contents))
	total := runningTotal
	for _, content := range contents {
		contentMap, ok := content.(map[string]any)
		if !ok {
			sanitized = append(sanitized, content)
			continue
		}
		contentCopy := copyMap(contentMap)
		if parts, ok := contentCopy["parts"].([]any); ok {
			newParts, stripped := sanitizeParts(parts, targetFamily, preserveNonSignature)
			contentCopy["parts"] = newParts
			total += stripped
		}
		sanitized = append(sanitized, contentCopy)
	}
	return sanitized, total
}

func sanitizeMessageList(messages []any, targetFamily ModelFamily, preserveNonSignature bool, runningTotal int) ([]any, int) {
	sanitized := make([]any, 0, len(messages))
	total := runningTotal
	for _, message := range messages {
		messageMap, ok := message.(map[string]any)
		if !ok {
			sanitized = append(sanitized, message)
			continue
		}
		messageCopy := copyMap(messageMap)
		if content, ok := messageCopy["content"].([]any); ok {
			newParts, stripped := sanitizeParts(content, targetFamily, preserveNonSignature)
			messageCopy["content"] = newParts
			total += stripped
		}
		sanitized = append(sanitized, messageCopy)
	}
	return sanitized, total
}

func sanitizeParts(parts []any, targetFamily ModelFamily, preserveNonSignature bool) ([]any, int) {
	total := 0
	sanitized := make([]any, 0, len(parts))
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			sanitized = append(sanitized, part)
			continue
		}
		partCopy := copyMap(partMap)
		stripped := 0
		if targetFamily == ModelFamilyClaude {
			partCopy, stripped = StripGeminiThinkingMetadata(partCopy, preserveNonSignature)
		} else if isGeminiTransformFamily(targetFamily) {
			partCopy, stripped = StripClaudeThinkingFields(partCopy)
		}
		total += stripped
		sanitized = append(sanitized, partCopy)
	}
	return sanitized, total
}

func isGeminiTransformFamily(family ModelFamily) bool {
	return family == ModelFamilyGeminiFlash || family == ModelFamilyGeminiPro
}

// ResolveModelWithTier resolves model aliases, quota preference, and thinking tier metadata.
func ResolveModelWithTier(requestedModel string, options ...ModelResolverOptions) ResolvedModel {
	option := ModelResolverOptions{}
	if len(options) > 0 {
		option = options[0]
	}
	isAntigravity := hasPrefixFold(requestedModel, "antigravity-")
	modelWithoutQuota := trimPrefixFold(requestedModel, "antigravity-")
	tier := extractThinkingTierFromModel(modelWithoutQuota)
	baseName := modelWithoutQuota
	if tier != "" {
		baseName = removeThinkingTierSuffix(modelWithoutQuota)
	}

	isImageModel := IsImageGenerationModel(modelWithoutQuota)
	isClaude := strings.Contains(strings.ToLower(modelWithoutQuota), "claude")
	preferGeminiCLI := option.CLIFirst && !isAntigravity && !isImageModel && !isClaude
	quotaPreference := HeaderStyleAntigravity
	if preferGeminiCLI {
		quotaPreference = HeaderStyleGeminiCLI
	}
	explicitQuota := isAntigravity || isImageModel

	lowerWithoutQuota := strings.ToLower(modelWithoutQuota)
	isGemini3 := strings.HasPrefix(lowerWithoutQuota, "gemini-3")
	skipAlias := isAntigravity && isGemini3
	isGemini31Pro := strings.HasPrefix(strings.ToLower(baseName), "gemini-3.1-pro")
	isGemini35Flash := isGemini35FlashModelName(baseName)
	isGemini36Flash := isGemini36FlashModelName(baseName)

	if isGemini31Pro && quotaPreference == HeaderStyleAntigravity {
		return ResolvedModel{ActualModel: getAgyGemini31ProModel(tier), ThinkingBudget: getAgyGemini31ProThinkingBudget(tier), Tier: tier, IsThinkingModel: true, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
	}
	if isGemini35Flash && quotaPreference == HeaderStyleAntigravity {
		resolvedTier := tier
		if resolvedTier == "" {
			resolvedTier = ThinkingTierMedium
		}
		return ResolvedModel{ActualModel: GetGemini35FlashAntigravityModel(string(resolvedTier)), ThinkingBudget: getAgyGeminiFlashThinkingBudget(resolvedTier), Tier: resolvedTier, IsThinkingModel: true, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
	}
	if isGemini36Flash && quotaPreference == HeaderStyleAntigravity {
		resolvedTier := tier
		if resolvedTier == "" {
			resolvedTier = ThinkingTierMedium
		}
		return ResolvedModel{ActualModel: GetGemini36FlashAntigravityModel(string(resolvedTier)), ThinkingBudget: getAgyGeminiFlashThinkingBudget(resolvedTier), Tier: resolvedTier, IsThinkingModel: true, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
	}

	antigravityModel := modelWithoutQuota
	if skipAlias && (isGemini3ProModelName(modelWithoutQuota) || isGemini3FlashModelName(modelWithoutQuota)) && tier == "" && !isImageModel {
		defaultTier := ThinkingTierMedium
		if isGemini3ProModelName(modelWithoutQuota) {
			defaultTier = ThinkingTierLow
		}
		antigravityModel = modelWithoutQuota + "-" + string(defaultTier)
	}

	actualModel := baseName
	if skipAlias {
		actualModel = antigravityModel
	} else if alias := resolverAliases[strings.ToLower(modelWithoutQuota)]; alias != "" {
		actualModel = alias
	} else if alias := resolverAliases[strings.ToLower(baseName)]; alias != "" {
		actualModel = alias
	}

	isThinking := isThinkingCapableModelName(actualModel)
	if isImageModel {
		return ResolvedModel{ActualModel: actualModel, IsThinkingModel: false, IsImageModel: true, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
	}

	isEffectiveGemini3 := strings.Contains(strings.ToLower(actualModel), "gemini-3")
	isClaudeThinking := (strings.Contains(strings.ToLower(actualModel), "claude") && strings.Contains(strings.ToLower(actualModel), "thinking")) ||
		(strings.Contains(lowerWithoutQuota, "claude") && strings.Contains(lowerWithoutQuota, "thinking")) ||
		lowerWithoutQuota == "gemini-claude-sonnet-4-6"

	if tier == "" {
		if isEffectiveGemini3 {
			level := ThinkingTierLow
			if isGemini35Flash {
				level = ThinkingTierMedium
			}
			return ResolvedModel{ActualModel: actualModel, ThinkingLevel: string(level), IsThinkingModel: true, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
		}
		if isClaudeThinking {
			return ResolvedModel{ActualModel: actualModel, ThinkingBudget: 1024, IsThinkingModel: true, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
		}
		return ResolvedModel{ActualModel: actualModel, IsThinkingModel: isThinking, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
	}

	if isEffectiveGemini3 {
		return ResolvedModel{ActualModel: actualModel, ThinkingLevel: string(tier), Tier: tier, IsThinkingModel: true, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
	}
	if isClaudeThinking {
		return ResolvedModel{ActualModel: actualModel, ThinkingBudget: 1024, Tier: tier, IsThinkingModel: true, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
	}

	budgetFamily := getBudgetFamilyName(actualModel)
	thinkingBudget := thinkingTierBudgets[budgetFamily][tier]
	return ResolvedModel{ActualModel: actualModel, ThinkingBudget: thinkingBudget, Tier: tier, IsThinkingModel: isThinking, QuotaPreference: quotaPreference, ExplicitQuota: explicitQuota}
}

// ResolveModelForHeaderStyle resolves model names when switching between Antigravity and Gemini CLI quota headers.
func ResolveModelForHeaderStyle(requestedModel string, headerStyle HeaderStyle) ResolvedModel {
	lower := strings.ToLower(requestedModel)
	if !strings.Contains(lower, "gemini-3") {
		return ResolveModelWithTier(requestedModel)
	}
	if headerStyle == HeaderStyleAntigravity {
		transformedModel := trimPrefixFold(requestedModel, "antigravity-")
		transformedModel = removeSuffixFold(transformedModel, "-preview-customtools")
		transformedModel = removeSuffixFold(transformedModel, "-preview")
		hasTier := hasThinkingTierSuffix(transformedModel)
		isImageModel := IsImageGenerationModel(transformedModel)
		isGemini35Flash := isGemini35FlashModelName(removeThinkingTierSuffix(transformedModel))
		if (isGemini3ProModelName(transformedModel) || isGemini3FlashModelName(transformedModel)) && !isGemini35Flash && !hasTier && !isImageModel {
			defaultTier := ThinkingTierMedium
			if isGemini3ProModelName(transformedModel) {
				defaultTier = ThinkingTierLow
			}
			transformedModel = transformedModel + "-" + string(defaultTier)
		}
		return ResolveModelWithTier("antigravity-" + transformedModel)
	}
	if headerStyle == HeaderStyleGeminiCLI {
		withoutQuota := trimPrefixFold(requestedModel, "antigravity-")
		requestedTier := extractThinkingTierFromModel(withoutQuota)
		transformedModel := removeThinkingTierSuffix(withoutQuota)
		hasPreviewSuffix := strings.Contains(strings.ToLower(transformedModel), "-preview")
		if isGemini35FlashModelName(transformedModel) {
			transformedModel = GetGemini35FlashGeminiCliFallbackModel()
		} else if !hasPreviewSuffix {
			transformedModel += "-preview"
		}
		resolved := ResolveModelWithTier(transformedModel, ModelResolverOptions{CLIFirst: true})
		if requestedTier != "" {
			resolved.ThinkingLevel = string(requestedTier)
			resolved.Tier = requestedTier
		}
		resolved.QuotaPreference = HeaderStyleGeminiCLI
		return resolved
	}
	return ResolveModelWithTier(requestedModel)
}

func supportsThinkingTiers(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "gemini-3") || strings.Contains(lower, "gemini-2.5") || (strings.Contains(lower, "claude") && strings.Contains(lower, "thinking"))
}

func extractThinkingTierFromModel(model string) ThinkingTier {
	if !supportsThinkingTiers(model) {
		return ""
	}
	lower := strings.ToLower(model)
	for _, tier := range []ThinkingTier{ThinkingTierMinimal, ThinkingTierLow, ThinkingTierMedium, ThinkingTierHigh} {
		if strings.HasSuffix(lower, "-"+string(tier)) {
			return tier
		}
	}
	return ""
}

func hasThinkingTierSuffix(model string) bool {
	return extractThinkingTierFromModel(model) != ""
}

func removeThinkingTierSuffix(model string) string {
	lower := strings.ToLower(model)
	for _, tier := range []ThinkingTier{ThinkingTierMinimal, ThinkingTierLow, ThinkingTierMedium, ThinkingTierHigh} {
		suffix := "-" + string(tier)
		if strings.HasSuffix(lower, suffix) {
			return model[:len(model)-len(suffix)]
		}
	}
	return model
}

func getBudgetFamilyName(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "claude"):
		return "claude"
	case strings.Contains(lower, "gemini-2.5-pro"):
		return "gemini-2.5-pro"
	case strings.Contains(lower, "gemini-2.5-flash"):
		return "gemini-2.5-flash"
	default:
		return "default"
	}
}

func isThinkingCapableModelName(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "thinking") || strings.Contains(lower, "gemini-3") || strings.Contains(lower, "gemini-2.5")
}

func isGemini3ProModelName(model string) bool {
	lower := strings.ToLower(model)
	return strings.HasPrefix(lower, "gemini-3-pro") || strings.HasPrefix(lower, "gemini-3.") && strings.Contains(lower, "-pro")
}

func isGemini3FlashModelName(model string) bool {
	lower := strings.ToLower(model)
	return strings.HasPrefix(lower, "gemini-3-flash") || strings.HasPrefix(lower, "gemini-3.") && strings.Contains(lower, "-flash")
}

func isGemini35FlashModelName(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "gemini-3.5-flash")
}

func isGemini36FlashModelName(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "gemini-3.6-flash")
}

// getAgyGeminiFlashThinkingBudget is shared by Gemini 3.5/3.6 Flash antigravity routes.
// Ported from antigravity-auth getAgyGeminiFlashThinkingBudget.
func getAgyGeminiFlashThinkingBudget(tier ThinkingTier) int {
	switch tier {
	case ThinkingTierLow:
		return 1000
	case ThinkingTierHigh:
		return 10000
	case ThinkingTierMedium, "":
		return 4000
	default:
		return 4000
	}
}

func getAgyGemini31ProModel(tier ThinkingTier) string {
	if tier == ThinkingTierHigh {
		return "gemini-pro-agent"
	}
	return "gemini-3.1-pro-low"
}

func getAgyGemini31ProThinkingBudget(tier ThinkingTier) int {
	if tier == ThinkingTierHigh {
		return 10001
	}
	return 1001
}

func hasPrefixFold(value, prefix string) bool {
	return strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix))
}

func trimPrefixFold(value, prefix string) string {
	if hasPrefixFold(value, prefix) {
		return value[len(prefix):]
	}
	return value
}

func removeSuffixFold(value, suffix string) string {
	if strings.HasSuffix(strings.ToLower(value), strings.ToLower(suffix)) {
		return value[:len(value)-len(suffix)]
	}
	return value
}

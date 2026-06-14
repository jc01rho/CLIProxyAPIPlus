package antigravity

import (
	"encoding/json"
	"fmt"
	"math"
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
	"claude-opus-4-6":           "claude",
	"claude-sonnet-4-6-thinking": "claude",
	"claude-sonnet-4-6":         "claude",
	"gemini-pro-agent":          "gemini-pro",
	"gemini-3.1-pro":            "gemini-pro",
	"gemini-3.1-pro-low":        "gemini-pro",
	"gemini-3.1-pro-high":       "gemini-pro",
	"gemini-3-flash":            "gemini-flash",
	"gemini-3-flash-agent":      "gemini-flash",
	"gemini-3.5-flash-low":      "gemini-flash",
	"gemini-3.5-flash-extra-low": "gemini-flash",
	"gemini-3.1-flash-image":    "gemini-flash",
	"gpt-oss-120b":              "gpt-oss",
	"gpt-oss-120b-medium":       "gpt-oss",
}

// antigravityOpenCodeModelIDs are the model IDs exposed by antigravity.
// Ported from antigravity-auth ANTIGRAVITY_OPENCODE_MODEL_IDS.
var antigravityOpenCodeModelIDs = []string{
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

// buildClaudeThinkingConfig builds the thinking config for Claude models with snake_case keys.
func buildClaudeThinkingConfig(includeThoughts bool, thinkingBudget int) map[string]any {
	result := map[string]any{
		"include_thoughts": includeThoughts,
	}
	if thinkingBudget > 0 {
		result["thinking_budget"] = thinkingBudget
	}
	return result
}

// ensureClaudeMaxOutputTokens ensures maxOutputTokens is sufficient for Claude thinking models.
// If thinking budget is set, max output must be larger than the budget.
func ensureClaudeMaxOutputTokens(generationConfig map[string]any, thinkingBudget int) {
	if thinkingBudget <= 0 {
		return
	}
	currentMax := 0
	if v, ok := generationConfig["maxOutputTokens"].(int); ok {
		currentMax = v
	}
	if v, ok := generationConfig["max_output_tokens"].(int); ok && currentMax == 0 {
		currentMax = v
	}
	if currentMax == 0 || currentMax <= thinkingBudget {
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
func appendClaudeThinkingHint(payload []byte) []byte {
	// Check if hint is already present
	sysValue := gjson.GetBytes(payload, "system").Raw
	if strings.Contains(sysValue, claudeInterleavedThinkingHint) {
		return payload
	}

	existing := gjson.GetBytes(payload, "system")
	switch existing.Type {
	case gjson.String:
		current := existing.String()
		hint := claudeInterleavedThinkingHint
		if strings.Contains(current, hint) {
			return payload
		}
		trimmed := strings.TrimSpace(current)
		if trimmed == "" {
			payload, _ = sjson.SetBytes(payload, "system", hint)
		} else {
			payload, _ = sjson.SetBytes(payload, "system", trimmed+"\n\n"+hint)
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
			payload, _ = sjson.SetRawBytes(payload, "system.parts", newPartJSON)
		} else {
			// Get existing parts array, append new part
			partsRaw := existing.Get("parts").Raw
			newParts := strings.TrimSuffix(partsRaw, "]") + "," + string(newPartJSON) + "]"
			payload, _ = sjson.SetRawBytes(payload, "system", []byte("{\"parts\":"+newParts+"}"))
		}
		return payload
	default:
		// No system instruction, create one
		payload, _ = sjson.SetBytes(payload, "system", claudeInterleavedThinkingHint)
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

package access

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const modelAccessPatternsContextKey = "apiKeyModelAccessPatterns"

// SetModelAccessPatterns stores the authenticated API key's model whitelist on the request.
func SetModelAccessPatterns(c *gin.Context, patterns []string) {
	if c == nil || len(patterns) == 0 {
		return
	}
	c.Set(modelAccessPatternsContextKey, append([]string(nil), patterns...))
}

// ModelAccessPatterns returns the model whitelist attached to the request.
func ModelAccessPatterns(c *gin.Context) []string {
	if c == nil {
		return nil
	}
	value, ok := c.Get(modelAccessPatternsContextKey)
	if !ok {
		return nil
	}
	patterns, ok := value.([]string)
	if !ok {
		return nil
	}
	return patterns
}

// ModelAllowed reports whether model matches at least one configured pattern.
// An empty pattern list allows every model. Only '*' has wildcard semantics.
func ModelAllowed(model string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	model = strings.TrimSpace(model)
	for _, pattern := range patterns {
		if wildcardMatch(model, strings.TrimSpace(pattern)) {
			return true
		}
	}
	return false
}

// FilterModelMaps filters model maps using their id or name field.
func FilterModelMaps(c *gin.Context, models []map[string]any) []map[string]any {
	patterns := ModelAccessPatterns(c)
	if len(patterns) == 0 {
		return models
	}
	filtered := make([]map[string]any, 0, len(models))
	for _, model := range models {
		modelID, _ := model["id"].(string)
		if modelID == "" {
			modelID, _ = model["name"].(string)
			modelID = strings.TrimPrefix(modelID, "models/")
		}
		if modelID != "" && ModelAllowed(modelID, patterns) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func wildcardMatch(value, pattern string) bool {
	if pattern == "" {
		return false
	}
	valueIndex, patternIndex := 0, 0
	starIndex, matchIndex := -1, 0
	for valueIndex < len(value) {
		if patternIndex < len(pattern) && pattern[patternIndex] == value[valueIndex] {
			valueIndex++
			patternIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			starIndex = patternIndex
			matchIndex = valueIndex
			patternIndex++
			continue
		}
		if starIndex >= 0 {
			patternIndex = starIndex + 1
			matchIndex++
			valueIndex = matchIndex
			continue
		}
		return false
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}

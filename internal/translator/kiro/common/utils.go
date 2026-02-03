// Package common provides shared constants and utilities for Kiro translator.
package common

import (
	"strings"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// GetString safely extracts a string from a map.
// Returns empty string if the key doesn't exist or the value is not a string.
func GetString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// GetStringValue is an alias for GetString for backward compatibility.
func GetStringValue(m map[string]interface{}, key string) string {
	return GetString(m, key)
}

// SanitizeToolUseID ensures tool_use.id matches Claude API pattern ^[a-zA-Z0-9_-]+$
// Returns sanitized ID or generates new one if input is invalid.
func SanitizeToolUseID(id string) string {
	if id == "" {
		return ""
	}

	var sanitized strings.Builder
	sanitized.Grow(len(id))

	for _, r := range id {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' {
			sanitized.WriteRune(r)
		}
	}

	result := sanitized.String()

	if len(result) < 8 {
		log.Warnf("kiro: tool_use.id '%s' sanitized to '%s' (too short), generating new ID", id, result)
		return GenerateToolUseID()
	}

	if result != id {
		log.Warnf("kiro: tool_use.id sanitized: '%s' -> '%s'", id, result)
	}

	return result
}

// GenerateToolUseID creates a valid tool_use.id without hyphens
func GenerateToolUseID() string {
	return "toolu_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
}

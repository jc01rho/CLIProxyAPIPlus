// Package common provides shared constants and utilities for Kiro translator.
package common

import (
	"crypto/rand"
	"strings"
)

const toolUseIDAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"

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

// GenerateToolUseID returns a Claude-compatible tool_use id.
func GenerateToolUseID() string {
	const randomLen = 12
	b := make([]byte, randomLen)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(i)
		}
	}
	var out strings.Builder
	out.Grow(len("toolu_") + randomLen)
	out.WriteString("toolu_")
	for _, v := range b {
		out.WriteByte(toolUseIDAlphabet[int(v)%len(toolUseIDAlphabet)])
	}
	return out.String()
}

// SanitizeToolUseID keeps only characters accepted by Claude tool_use ids.
func SanitizeToolUseID(id string) string {
	var out strings.Builder
	out.Grow(len(id))
	for _, r := range id {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' {
			out.WriteRune(r)
		}
	}
	if id != "" && out.Len() < 8 {
		return GenerateToolUseID()
	}
	return out.String()
}

package oauthform

import (
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// Pair is one ordered form or query parameter.
type Pair struct {
	Key   string
	Value string
}

// Encode preserves pair order while matching application/x-www-form-urlencoded escaping.
func Encode(pairs ...Pair) string {
	if len(pairs) == 0 {
		return ""
	}
	encoded := make([]byte, 0, len(pairs)*16)
	for i, pair := range pairs {
		if i > 0 {
			encoded = append(encoded, '&')
		}
		encoded = append(encoded, url.QueryEscape(pair.Key)...)
		encoded = append(encoded, '=')
		encoded = append(encoded, url.QueryEscape(pair.Value)...)
	}
	return string(encoded)
}

// MaskSensitive masks sensitive values in an encoded form/query string while preserving order.
func MaskSensitive(encoded string) string {
	if encoded == "" {
		return ""
	}
	parts := strings.Split(encoded, "&")
	changed := false
	for i, part := range parts {
		if part == "" {
			continue
		}
		keyPart := part
		valuePart := ""
		if idx := strings.Index(part, "="); idx >= 0 {
			keyPart = part[:idx]
			valuePart = part[idx+1:]
		}
		decodedKey, err := url.QueryUnescape(keyPart)
		if err != nil || !IsSensitiveKey(decodedKey) {
			continue
		}
		decodedValue, err := url.QueryUnescape(valuePart)
		if err != nil {
			decodedValue = valuePart
		}
		parts[i] = keyPart + "=" + url.QueryEscape(util.HideAPIKey(strings.TrimSpace(decodedValue)))
		changed = true
	}
	if !changed {
		return encoded
	}
	return strings.Join(parts, "&")
}

// IsSensitiveKey reports whether an OAuth form/query field can contain a credential.
func IsSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	key = strings.TrimSuffix(key, "[]")
	return strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "verifier")
}

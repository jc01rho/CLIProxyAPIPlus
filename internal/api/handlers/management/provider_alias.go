package management

import "strings"

func canonicalizeAuthProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "kilocode" {
		return "kilo"
	}
	if provider == "" {
		return "unknown"
	}
	return provider
}

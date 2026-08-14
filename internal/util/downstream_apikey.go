package util

import (
	"fmt"
	"net/http"
	"strings"
)

// ExtractDownstreamAPIKey returns the original downstream client's API key so
// operators can identify the exact configured credential that triggered a
// WARN/ERROR access or request-failed log.
func ExtractDownstreamAPIKey(header http.Header) string {
	if len(header) == 0 {
		return ""
	}
	candidates := []struct {
		header string
		scheme string
	}{
		{"Authorization", "Bearer"},
		{"X-Api-Key", "X-Api-Key"},
		{"Api-Key", "X-Api-Key"},
		{"X-Goog-Api-Key", "X-Goog-Api-Key"},
	}
	for _, candidate := range candidates {
		raw := strings.TrimSpace(header.Get(candidate.header))
		if raw == "" {
			continue
		}
		secret := raw
		if candidate.scheme == "Bearer" {
			idx := strings.Index(raw, " ")
			if idx > 0 {
				scheme := strings.ToLower(strings.TrimSpace(raw[:idx]))
				if scheme != "bearer" {
					return ""
				}
				secret = strings.TrimSpace(raw[idx+1:])
			} else {
				// "Bearer" without a following space-separated token means the
				// caller sent a malformed header (e.g., "Bearer " or "Bearer").
				// Treat as no token to avoid emitting a misleading preview.
				continue
			}
		}
		if secret == "" {
			return ""
		}
		return fmt.Sprintf("%s(%s)", candidate.scheme, secret)
	}
	return ""
}

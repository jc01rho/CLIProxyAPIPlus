package oauthlogin

import (
	"net/url"
	"strings"
)

// RedactURL returns a log-safe representation of rawURL: the scheme, host and
// path are preserved while every query parameter value and the fragment are
// replaced with a placeholder. OAuth codes, state values and session tokens
// therefore never reach a log file or an error message.
func RedactURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		// Fall back to a dumb split so a malformed URL still cannot leak.
		if i := strings.IndexAny(trimmed, "?#"); i >= 0 {
			return trimmed[:i] + "?[redacted]"
		}
		return trimmed
	}
	// The output is assembled by hand: url.URL.String percent-escapes the
	// placeholder inside the user info and the fragment.
	var sb strings.Builder
	if u.Scheme != "" {
		sb.WriteString(u.Scheme)
		sb.WriteString(":")
	}
	if u.Host != "" || u.User != nil || strings.HasPrefix(trimmed, u.Scheme+"://") {
		sb.WriteString("//")
	}
	if u.User != nil {
		sb.WriteString("[redacted]@")
	}
	sb.WriteString(u.Host)
	sb.WriteString(u.EscapedPath())
	if u.Opaque != "" {
		sb.WriteString(u.Opaque)
	}
	if u.ForceQuery || u.RawQuery != "" {
		sb.WriteString("?")
		keys := make([]string, 0, 8)
		for _, pair := range strings.Split(u.RawQuery, "&") {
			if pair == "" {
				continue
			}
			key := pair
			if i := strings.Index(pair, "="); i >= 0 {
				key = pair[:i]
			}
			keys = append(keys, key+"=[redacted]")
		}
		sb.WriteString(strings.Join(keys, "&"))
	}
	if u.Fragment != "" {
		sb.WriteString("#[redacted]")
	}
	return sb.String()
}

// redactText scrubs every URL-looking token inside an arbitrary message.
func redactText(msg string) string {
	fields := strings.Fields(msg)
	changed := false
	for i, f := range fields {
		lower := strings.ToLower(f)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			red := RedactURL(f)
			if red != f {
				fields[i] = red
				changed = true
			}
		}
	}
	if !changed {
		return msg
	}
	return strings.Join(fields, " ")
}

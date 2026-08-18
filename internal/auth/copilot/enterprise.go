package copilot

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeEnterpriseDomain converts a GitHub Enterprise URL or bare domain
// (e.g. "https://company.ghe.com" or "company.ghe.com") into a plain hostname.
// An empty input returns an empty string (meaning github.com). Non-empty input
// that cannot be parsed as a URL with a hostname returns an error.
//
// Mirrors senpi's normalizeDomain() in packages/ai/src/auth/oauth/github-copilot.ts.
func NormalizeEnterpriseDomain(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", nil
	}
	raw := trimmed
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid GitHub Enterprise URL/domain: %s", trimmed)
	}
	return parsed.Hostname(), nil
}

// resolveDomain returns the effective GitHub domain, defaulting to github.com.
func resolveDomain(domain string) string {
	if d := strings.TrimSpace(domain); d != "" {
		return d
	}
	return "github.com"
}

// IsGitHubDotCom reports whether the domain is the public github.com service.
func IsGitHubDotCom(domain string) bool {
	return strings.EqualFold(strings.TrimSpace(domain), "github.com") || strings.TrimSpace(domain) == ""
}

// DeviceCodeURLForDomain returns the OAuth device code endpoint for a domain.
func DeviceCodeURLForDomain(domain string) string {
	return fmt.Sprintf("https://%s/login/device/code", resolveDomain(domain))
}

// AccessTokenURLForDomain returns the OAuth access token endpoint for a domain.
func AccessTokenURLForDomain(domain string) string {
	return fmt.Sprintf("https://%s/login/oauth/access_token", resolveDomain(domain))
}

// UserInfoURLForDomain returns the GitHub user profile API endpoint for a domain.
func UserInfoURLForDomain(domain string) string {
	return fmt.Sprintf("https://api.%s/user", resolveDomain(domain))
}

// CopilotAPITokenURLForDomain returns the copilot_internal token exchange
// endpoint for a domain.
func CopilotAPITokenURLForDomain(domain string) string {
	return fmt.Sprintf("https://api.%s/copilot_internal/v2/token", resolveDomain(domain))
}

// FallbackAPIEndpointForDomain returns the Copilot API base URL fallback used
// when the token response carries no usable endpoints.api value. Enterprise
// accounts use https://copilot-api.{domain} (senpi's enterprise fallback);
// github.com keeps the historical default.
func FallbackAPIEndpointForDomain(domain string) string {
	if IsGitHubDotCom(domain) {
		return copilotAPIEndpoint
	}
	return fmt.Sprintf("https://copilot-api.%s", resolveDomain(domain))
}

// isAllowedCopilotHostForDomain reports whether host is a trusted Copilot API
// host for the given credential domain. Public github.com credentials are
// limited to the static allowlist; enterprise credentials additionally trust
// the domain-derived api./copilot-api./proxy. hosts.
func isAllowedCopilotHostForDomain(host, domain string) bool {
	if allowedCopilotAPIHosts[host] {
		return true
	}
	if IsGitHubDotCom(domain) {
		return false
	}
	d := resolveDomain(domain)
	return host == "api."+d || host == "copilot-api."+d || host == "proxy."+d
}

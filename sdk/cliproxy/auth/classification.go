package auth

import (
	"net/url"
	"strings"
)

const (
	AuthKindAPIKey = "apikey"
	AuthKindOAuth  = "oauth"

	AuthSourceConfig      = "config"
	AuthSourceFile        = "file"
	AuthSourceGit         = "git"
	AuthSourceMemory      = "memory"
	AuthSourceObjectStore = "objectstore"
	AuthSourcePostgres    = "postgres"

	AttributeAPIKey           = "api_key"
	AttributeAuthKind         = "auth_kind"
	AttributeCodexAlphaSearch = "codex_alpha_search"
	AttributeConfigIndex      = "config_index"
	AttributePath             = "path"
	AttributeRuntimeOnly      = "runtime_only"
	AttributeSource           = "source"
	AttributeSourceBackend    = "source_backend"
	AttributeWeight           = "weight"
)

// AuthKind returns the credential kind using explicit metadata first and legacy
// field-shape fallbacks second.
func (a *Auth) AuthKind() string {
	if a == nil {
		return ""
	}
	if kind := normalizeAuthKind(authAttribute(a, AttributeAuthKind)); kind != "" {
		return kind
	}
	if kind := normalizeAuthKind(authMetadataString(a, AttributeAuthKind)); kind != "" {
		return kind
	}
	if authAttribute(a, AttributeAPIKey) != "" {
		return AuthKindAPIKey
	}
	if authHasOAuthMetadata(a) {
		return AuthKindOAuth
	}
	return ""
}

// AuthSourceKind returns where the Auth entry came from at runtime.
func (a *Auth) AuthSourceKind() string {
	if a == nil {
		return ""
	}
	if strings.EqualFold(authAttribute(a, AttributeRuntimeOnly), "true") {
		return AuthSourceMemory
	}
	if source := normalizeAuthSourceKind(authAttribute(a, AttributeSourceBackend)); source != "" {
		return source
	}
	source := authAttribute(a, AttributeSource)
	if source != "" {
		sourceLower := strings.ToLower(source)
		if strings.HasPrefix(sourceLower, AuthSourceConfig+":") {
			return AuthSourceConfig
		}
		if normalized := normalizeAuthSourceKind(source); normalized != "" {
			return normalized
		}
		return AuthSourceFile
	}
	if authAttribute(a, AttributePath) != "" {
		return AuthSourceFile
	}
	if strings.TrimSpace(a.FileName) != "" {
		return AuthSourceFile
	}
	return ""
}

func normalizeAuthKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case AuthKindAPIKey, "api_key", "api-key":
		return AuthKindAPIKey
	case AuthKindOAuth, "oauth2":
		return AuthKindOAuth
	default:
		return ""
	}
}

func normalizeAuthSourceKind(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case AuthSourceConfig:
		return AuthSourceConfig
	case AuthSourceFile, "filesystem":
		return AuthSourceFile
	case AuthSourceGit:
		return AuthSourceGit
	case AuthSourceMemory, "runtime", "runtime_only":
		return AuthSourceMemory
	case AuthSourceObjectStore, "object-store":
		return AuthSourceObjectStore
	case AuthSourcePostgres, "postgresql", "database", "db":
		return AuthSourcePostgres
	default:
		return ""
	}
}

func authHasOAuthMetadata(auth *Auth) bool {
	if auth == nil || len(auth.Metadata) == 0 {
		return false
	}
	for _, key := range []string{"access_token", "refresh_token", "id_token", "email", "token_type", "expires_at", "expired"} {
		if authMetadataString(auth, key) != "" {
			return true
		}
	}
	if token, ok := auth.Metadata["token"].(map[string]any); ok && len(token) > 0 {
		return true
	}
	return false
}

// authHasCustomBaseURL reports whether the credential pins a non-default API
// base URL (for example a third-party Claude-compatible mirror such as
// https://claude.nekos.me). Custom-base credentials must not trigger token
// refresh: the refresh exchange targets the official Anthropic OAuth token
// endpoint, which neither knows the mirror's credentials nor returns tokens
// the mirror would accept.
func authHasCustomBaseURL(a *Auth) bool {
	if a == nil || a.Attributes == nil {
		return false
	}
	raw := strings.TrimSpace(a.Attributes["base_url"])
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" {
		// An unparseable value is still an explicit override; treat it as
		// custom so it never silently falls back to official refresh.
		return true
	}
	return !strings.EqualFold(u.Hostname(), "api.anthropic.com")
}

func authAttribute(auth *Auth, key string) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[key])
}

func authMetadataString(auth *Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	switch value := auth.Metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

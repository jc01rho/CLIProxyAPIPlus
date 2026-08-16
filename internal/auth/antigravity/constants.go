// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

// OAuth client credentials and configuration
const (
	ClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	ClientSecret = "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"
	CallbackPort = 51121

	// RedirectURI is the redirect URI registered with Google for the local CLI
	// callback server. Antigravity rejects any other value, so the CLI must
	// never rewrite it from --oauth-callback-port.
	RedirectURI = "http://localhost:51121/oauth-callback"
)

// Scopes defines the OAuth scopes required for Antigravity authentication
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// OAuth2 endpoints for Google authentication
const (
	TokenEndpoint    = "https://oauth2.googleapis.com/token"
	AuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	UserInfoEndpoint = "https://www.googleapis.com/oauth2/v1/userinfo?alt=json"
)

// Antigravity API configuration
const (
	APIEndpoint        = "https://cloudcode-pa.googleapis.com"
	DailyAPIEndpoint   = "https://daily-cloudcode-pa.googleapis.com"
	APIVersion         = "v1internal"
	GeminicliUserAgent = "google-api-nodejs-client/9.15.1"
)

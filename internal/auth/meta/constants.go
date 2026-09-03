// Package meta provides OAuth2 device-code authentication helpers for the Meta
// Model API (Muse Spark) subscription flow.
//
// The Meta subscription flow mirrors the Muse Code CLI: a device-code OAuth
// login against auth.meta.com yields an access token, which is then exchanged
// ("minted") for a Model API key at api.meta.ai/muse-code/key. The minted key
// is a bearer credential that works against the OpenAI-compatible endpoint
// https://api.meta.ai/v1.
package meta

import "time"

const (
	// AuthURL is the Meta OAuth authority.
	AuthURL = "https://auth.meta.com"
	// AuthorizationEndpoint is the OAuth2 device authorization endpoint.
	AuthorizationEndpoint = "/oidc/device/authorization/"
	// TokenEndpoint is the OAuth2 device token endpoint.
	TokenEndpoint = "/oidc/device/token/"
	// DeviceCodeGrantType is the OAuth2 device authorization grant type (RFC 8628).
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// ClientID is the public Muse Code OAuth client ID.
	ClientID = "1031625952748946"
	// ProviderName is the config identifier for the Meta Model API provider.
	ProviderName = "meta"
	// MintURL is the endpoint that exchanges a subscription access token for a
	// Model API key. It is reverse-engineered from the Muse Code CLI and is not
	// part of Meta's public API documentation.
	MintURL = "https://api.meta.ai/muse-code/key"
	// APIBaseURL is the OpenAI-compatible Model API base URL.
	APIBaseURL = "https://api.meta.ai/v1"
	// UserAgent is the client identifier sent on OAuth requests.
	UserAgent = "muse-code/launcher-2"

	// defaultPollInterval is used when the device endpoint omits interval.
	// RFC 8628 section 3.5 also mandates this as the slow_down backoff increment.
	defaultPollInterval = 5 * time.Second
	// minPollInterval is the floor applied to a server-reported slow_down interval.
	minPollInterval = 1 * time.Second
	// httpClientTimeout bounds credential-acquisition HTTP calls (device/token/mint).
	httpClientTimeout = 30 * time.Second
	// MaxPollDuration is the upper bound for waiting on user authorization.
	MaxPollDuration = 30 * time.Minute
)

// DeviceCodeResponse represents Meta's device authorization response.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// MintedKey is the response from the mint endpoint. The API key is a bearer
// credential valid against the OpenAI-compatible Model API base URL.
type MintedKey struct {
	APIKey string `json:"api_key"`
}

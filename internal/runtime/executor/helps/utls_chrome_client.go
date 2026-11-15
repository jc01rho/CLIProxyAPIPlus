package helps

import (
	"context"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

// chromeSchemeRoundTripper sends https requests through the Chrome-fingerprint
// transport and anything else through the standard transport, because the uTLS
// dialer only speaks TLS.
type chromeSchemeRoundTripper struct {
	chrome   http.RoundTripper
	fallback http.RoundTripper
}

func (t *chromeSchemeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil && req.URL.Scheme == "https" {
		return t.chrome.RoundTrip(req)
	}
	return t.fallback.RoundTrip(req)
}

// NewChromeHTTPClient creates an HTTP client that presents a Chrome TLS
// fingerprint to every https host, honoring the same proxy priority as
// NewProxyAwareHTTPClient. Use it for upstreams that block the Go TLS profile.
func NewChromeHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	proxyURL := effectiveProxyURL(ctx, cfg, auth)
	var fallback http.RoundTripper = http.DefaultTransport
	if transport := pooledProxyTransport(proxyURL, cfg, auth); transport != nil {
		fallback = transport
	}
	client := &http.Client{Transport: &chromeSchemeRoundTripper{chrome: newUtlsRoundTripper(proxyURL), fallback: fallback}}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return httptransport.Decorate(ctx, client)
}

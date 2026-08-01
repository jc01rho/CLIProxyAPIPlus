package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// httpClientCache caches HTTP clients by proxy URL to enable connection reuse
var (
	httpClientCache      = make(map[string]*http.Client)
	httpClientCacheMutex sync.RWMutex
)

const (
	defaultTLSHandshakeTimeout   = 30 * time.Second
	defaultResponseHeaderTimeout = 45 * time.Second
	defaultStreamingIdleTimeout  = 5 * time.Minute
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// This function caches HTTP clients by proxy URL to enable TCP/TLS connection reuse.
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURL if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If we have a proxy URL configured, try cache first to reuse TCP/TLS connections.
	if proxyURL != "" {
		httpClientCacheMutex.RLock()
		if cachedClient, ok := httpClientCache[proxyURL]; ok {
			httpClientCacheMutex.RUnlock()
			if timeout > 0 {
				return &http.Client{Transport: cachedClient.Transport, Timeout: timeout}
			}
			return cachedClient
		}
		httpClientCacheMutex.RUnlock()
	}

	// Create new client
	httpClient := &http.Client{Transport: newBoundedTransport()}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = withStreamingIdleTimeout(transport)
			// Cache the client
			httpClientCacheMutex.Lock()
			httpClientCache[proxyURL] = httpClient
			httpClientCacheMutex.Unlock()
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			httpClient.Transport = withStreamingIdleTimeout(rt)
		}
	}

	return httpClient
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	tuneHTTPTransport(transport)
	return transport
}

func newBoundedTransport() http.RoundTripper {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || transport == nil {
		transport = &http.Transport{}
	}
	clone := transport.Clone()
	tuneHTTPTransport(clone)
	return withStreamingIdleTimeout(clone)
}

func tuneHTTPTransport(transport *http.Transport) {
	if transport == nil {
		return
	}
	if transport.ResponseHeaderTimeout == 0 {
		transport.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	}
	if transport.TLSHandshakeTimeout == 0 {
		transport.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
	}
}

func withStreamingIdleTimeout(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return idleTimeoutRoundTripper{next: rt, idleTimeout: defaultStreamingIdleTimeout}
}

type idleTimeoutRoundTripper struct {
	next        http.RoundTripper
	idleTimeout time.Duration
}

func (rt idleTimeoutRoundTripper) Unwrap() http.RoundTripper {
	return rt.next
}

func (rt idleTimeoutRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.next.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil || rt.idleTimeout <= 0 {
		return resp, err
	}
	resp.Body = newIdleTimeoutBody(resp.Body, rt.idleTimeout)
	return resp, nil
}

type idleTimeoutBody struct {
	body    io.ReadCloser
	timeout time.Duration
	once    sync.Once
	err     error
}

func newIdleTimeoutBody(body io.ReadCloser, timeout time.Duration) io.ReadCloser {
	return &idleTimeoutBody{body: body, timeout: timeout}
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	if b == nil || b.body == nil {
		return 0, http.ErrBodyReadAfterClose
	}
	timer := time.AfterFunc(b.timeout, func() {
		_ = b.body.Close()
	})
	defer timer.Stop()
	return b.body.Read(p)
}

func (b *idleTimeoutBody) Close() error {
	if b == nil || b.body == nil {
		return nil
	}
	b.once.Do(func() {
		b.err = b.body.Close()
	})
	return b.err
}

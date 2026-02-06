package executor

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// httpClientCache caches HTTP clients by proxy URL to enable connection reuse
var (
	httpClientCache      = make(map[string]*http.Client)
	httpClientCacheMutex sync.RWMutex
)

// Default timeout constants for HTTP client transport
const (
	// defaultDialTimeout is the timeout for establishing TCP connections
	defaultDialTimeout = 30 * time.Second
	// defaultKeepAlive is the TCP keep-alive interval
	defaultKeepAlive = 30 * time.Second
	// defaultTLSHandshakeTimeout is the timeout for TLS handshake
	defaultTLSHandshakeTimeout = 10 * time.Second
	// defaultResponseHeaderTimeout is the timeout for receiving response headers
	// This timeout only applies AFTER the request is sent - it does NOT affect streaming body reads
	defaultResponseHeaderTimeout = 60 * time.Second
	// defaultIdleConnTimeout is how long idle connections stay in the pool
	defaultIdleConnTimeout = 90 * time.Second
	// defaultExpectContinueTimeout is the timeout for 100-continue responses
	defaultExpectContinueTimeout = 1 * time.Second
)

// newProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// This function caches HTTP clients by proxy URL to enable TCP/TLS connection reuse.
//
// IMPORTANT: For streaming responses (SSE, AI model outputs), Client.Timeout is NOT set.
// Instead, we use Transport-level timeouts (ResponseHeaderTimeout, DialTimeout) which
// only apply to connection establishment and header reception, NOT to body reading.
// This prevents "context deadline exceeded" errors during long-running streaming responses.
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means streaming-safe mode with no body read timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func newProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURL if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// Build cache key from proxy URL (empty string for no proxy)
	cacheKey := proxyURL

	// Check cache first
	httpClientCacheMutex.RLock()
	if cachedClient, ok := httpClientCache[cacheKey]; ok {
		httpClientCacheMutex.RUnlock()
		if timeout > 0 {
			return &http.Client{
				Transport: cachedClient.Transport,
				Timeout:   timeout,
			}
		}
		return cachedClient
	}
	httpClientCacheMutex.RUnlock()

	httpClient := &http.Client{}

	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			// Cache the client
			httpClientCacheMutex.Lock()
			httpClientCache[cacheKey] = httpClient
			httpClientCacheMutex.Unlock()
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	} else {
		httpClient.Transport = buildDefaultTransport()
	}

	// Cache the client for no-proxy case
	if proxyURL == "" {
		httpClientCacheMutex.Lock()
		httpClientCache[cacheKey] = httpClient
		httpClientCacheMutex.Unlock()
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
	if proxyURL == "" {
		return nil
	}

	parsedURL, errParse := url.Parse(proxyURL)
	if errParse != nil {
		log.Errorf("parse proxy URL failed: %v", errParse)
		return nil
	}

	var transport *http.Transport

	if parsedURL.Scheme == "socks5" {
		var proxyAuth *proxy.Auth
		if parsedURL.User != nil {
			username := parsedURL.User.Username()
			password, _ := parsedURL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		dialer, errSOCKS5 := proxy.SOCKS5("tcp", parsedURL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.Errorf("create SOCKS5 dialer failed: %v", errSOCKS5)
			return nil
		}
		transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
			TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
			ResponseHeaderTimeout: defaultResponseHeaderTimeout,
			IdleConnTimeout:       defaultIdleConnTimeout,
			ExpectContinueTimeout: defaultExpectContinueTimeout,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ForceAttemptHTTP2:     true,
		}
	} else if parsedURL.Scheme == "http" || parsedURL.Scheme == "https" {
		transport = &http.Transport{
			Proxy: http.ProxyURL(parsedURL),
			DialContext: (&net.Dialer{
				Timeout:   defaultDialTimeout,
				KeepAlive: defaultKeepAlive,
			}).DialContext,
			TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
			ResponseHeaderTimeout: defaultResponseHeaderTimeout,
			IdleConnTimeout:       defaultIdleConnTimeout,
			ExpectContinueTimeout: defaultExpectContinueTimeout,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ForceAttemptHTTP2:     true,
		}
	} else {
		log.Errorf("unsupported proxy scheme: %s", parsedURL.Scheme)
		return nil
	}

	return transport
}

// buildDefaultTransport creates an HTTP transport with streaming-safe timeout settings.
// ResponseHeaderTimeout protects against unresponsive servers without affecting body reads.
func buildDefaultTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   defaultDialTimeout,
			KeepAlive: defaultKeepAlive,
		}).DialContext,
		TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
		ResponseHeaderTimeout: defaultResponseHeaderTimeout,
		IdleConnTimeout:       defaultIdleConnTimeout,
		ExpectContinueTimeout: defaultExpectContinueTimeout,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		ForceAttemptHTTP2:     true,
	}
}

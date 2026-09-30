package helps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
)

// httpClientCache caches HTTP clients by proxy URL to enable connection reuse
var (
	httpClientCache      = make(map[string]*http.Client)
	httpClientCacheMutex sync.RWMutex
	kiroProxyChainMu     sync.Mutex
	kiroProxyCooling     = make(map[string]time.Time)
	kiroProxyTransports  = NewTransportCache[string](DefaultTransportCacheCapacity)
)

const (
	defaultTLSHandshakeTimeout   = 30 * time.Second
	defaultResponseHeaderTimeout = 45 * time.Second
	defaultStreamingIdleTimeout  = 5 * time.Minute
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use the execution-scoped request proxy if configured (highest priority)
// 2. Use auth.ProxyURL if configured
// 3. Use cfg.ProxyURL if auth proxy is not configured
// 4. Use RoundTripper from context if none are configured
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
	// Priority: request override, then auth.ProxyURL, then cfg.ProxyURL.
	proxyURL := effectiveProxyURL(ctx, cfg, auth)

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

// NewKiroProxyChainHTTPClient builds a Kiro-only ordered proxy chain. A
// transport failure cools that proxy for 60 seconds and the next ready proxy
// is attempted. Ported from kiro-lb src/upstream/http.rs (1581af9).
func NewKiroProxyChainHTTPClient(timeout time.Duration, proxyURLs []string) *http.Client {
	transports := make([]kiroProxyTransport, 0, len(proxyURLs))
	seen := make(map[string]struct{}, len(proxyURLs))
	for _, raw := range proxyURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return &http.Client{Transport: kiroProxyConfigError{fmt.Errorf("kiro proxy-urls: empty entry; use direct for an explicit direct connection")}, Timeout: timeout}
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		transport, err := kiroProxyTransports.Get(raw, func() (*http.Transport, error) {
			transport, _, err := proxyutil.BuildHTTPTransport(raw)
			if err != nil {
				return nil, fmt.Errorf("kiro proxy-urls (%s): %w", proxyutil.Redact(raw), err)
			}
			tuneHTTPTransport(transport)
			if err := configureKiroProxyHTTP2(transport); err != nil {
				return nil, err
			}
			return transport, nil
		})
		if err != nil {
			return &http.Client{Transport: kiroProxyConfigError{err}, Timeout: timeout}
		}
		transports = append(transports, kiroProxyTransport{key: raw, transport: withStreamingIdleTimeout(transport)})
	}
	return &http.Client{Transport: kiroProxyChainRoundTripper{transports: transports}, Timeout: timeout}
}

type kiroProxyConfigError struct{ err error }

func (rt kiroProxyConfigError) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		if err := req.Body.Close(); err != nil {
			return nil, errors.Join(rt.err, err)
		}
	}
	return nil, rt.err
}

type kiroProxyTransport struct {
	key       string
	transport http.RoundTripper
}

type kiroProxyChainRoundTripper struct{ transports []kiroProxyTransport }

func (rt kiroProxyChainRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ordered := make([]kiroProxyTransport, 0, len(rt.transports))
	var cooling []kiroProxyTransport
	now := time.Now()
	kiroProxyChainMu.Lock()
	for _, candidate := range rt.transports {
		if until, ok := kiroProxyCooling[candidate.key]; ok && until.After(now) {
			cooling = append(cooling, candidate)
		} else {
			delete(kiroProxyCooling, candidate.key)
			ordered = append(ordered, candidate)
		}
	}
	ordered = append(ordered, cooling...)
	kiroProxyChainMu.Unlock()
	var lastErr error
	for i, candidate := range ordered {
		attempt := req.Clone(req.Context())
		if i > 0 && req.Body != nil && req.Body != http.NoBody {
			if req.GetBody == nil {
				return nil, fmt.Errorf("kiro: cannot replay request through next proxy: %w", lastErr)
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("kiro: replay proxy request body: %w", err)
			}
			attempt.Body = body
		}
		resp, err := candidate.transport.RoundTrip(attempt)
		if err == nil {
			kiroProxyChainMu.Lock()
			delete(kiroProxyCooling, candidate.key)
			kiroProxyChainMu.Unlock()
			return resp, nil
		}
		lastErr = err
		if req.Context().Err() != nil {
			return nil, err
		}
		kiroProxyChainMu.Lock()
		kiroProxyCooling[candidate.key] = time.Now().Add(time.Minute)
		kiroProxyChainMu.Unlock()
	}
	if lastErr == nil {
		return nil, fmt.Errorf("kiro: no proxy in proxy chain is available")
	}
	return nil, lastErr
}

// NewStandardHTTPClient uses the standard transport without response or stream
// timeouts while honoring the same request, credential, and global proxy priority.
func NewStandardHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) *http.Client {
	proxyURL := effectiveProxyURL(ctx, cfg, auth)
	if proxyURL != "" {
		transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
		if err != nil {
			log.Errorf("failed to configure proxy %q: %v", proxyutil.Redact(proxyURL), err)
		} else if transport != nil {
			return &http.Client{Transport: transport}
		}
	}
	if ctx != nil {
		if transport, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && transport != nil {
			return &http.Client{Transport: transport}
		}
	}
	return &http.Client{Transport: http.DefaultTransport}
}

var devinTransportCache = NewTransportCache[string](DefaultTransportCacheCapacity)

// NewDevinHTTPClient creates an HTTP client customized for Devin Connect-RPC upstream.
// Suppresses automatic Accept-Encoding: gzip while preserving connection reuse across requests.
func NewDevinHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// A request proxy replaces both the injected round tripper and credential/global proxy.
	// Respect explicitly injected context RoundTripper only when no request override is set.
	if cliproxyexecutor.RequestProxyURL(ctx) == "" && ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			if tr, ok := rt.(*http.Transport); ok {
				key := fmt.Sprintf("rt:%p", tr)
				cloned, err := devinTransportCache.Get(key, func() (*http.Transport, error) {
					c := tr.Clone()
					c.DisableCompression = true
					return c, nil
				})
				if err == nil && cloned != nil {
					return &http.Client{
						Transport: cloned,
						Timeout:   timeout,
					}
				}
			}
			return &http.Client{
				Transport: devinNoGzipRoundTripper{base: rt},
				Timeout:   timeout,
			}
		}
	}

	proxyURL := effectiveProxyURL(ctx, cfg, auth)

	tr, err := devinTransportCache.Get(proxyURL, func() (*http.Transport, error) {
		var base *http.Transport
		if proxyURL != "" {
			base = buildProxyTransport(proxyURL)
		}
		if base == nil {
			if dt, ok := http.DefaultTransport.(*http.Transport); ok {
				base = dt.Clone()
			} else {
				base = &http.Transport{}
			}
		}
		base.DisableCompression = true
		return base, nil
	})
	if err != nil || tr == nil {
		tr = &http.Transport{DisableCompression: true}
	}

	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
}

type devinNoGzipRoundTripper struct {
	base http.RoundTripper
}

func (rt devinNoGzipRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "identity")
	}
	return rt.base.RoundTrip(req)
}

func effectiveProxyURL(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) string {
	if proxyURL := cliproxyexecutor.RequestProxyURL(ctx); proxyURL != "" {
		return proxyURL
	}
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
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

func configureKiroProxyHTTP2(transport *http.Transport) error {
	transport.IdleConnTimeout = 30 * time.Minute
	transport.ForceAttemptHTTP2 = true
	h2Transport, err := http2.ConfigureTransports(transport)
	if err != nil {
		return fmt.Errorf("kiro: configure proxy HTTP/2: %w", err)
	}
	h2Transport.ReadIdleTimeout = 20 * time.Second
	h2Transport.PingTimeout = 10 * time.Second
	return nil
}

func withStreamingIdleTimeout(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return idleTimeoutRoundTripper{next: rt, idleTimeout: defaultStreamingIdleTimeout}
}

// UnwrapStreamingIdleTimeout strips the streaming-idle-timeout wrapper that
// NewProxyAwareHTTPClient layers on top of every transport. Callers that need
// to inspect the underlying *http.Transport (e.g. antigravity's HTTP/1.1
// fingerprint enforcement) must go through this helper instead of a raw type
// assertion, otherwise the wrapper hides the concrete type.
func UnwrapStreamingIdleTimeout(rt http.RoundTripper) http.RoundTripper {
	for {
		if u, ok := rt.(interface{ Unwrap() http.RoundTripper }); ok {
			inner := u.Unwrap()
			if inner == nil {
				return rt
			}
			rt = inner
			continue
		}
		return rt
	}
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

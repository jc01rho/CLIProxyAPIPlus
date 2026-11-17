package helps

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewProxyAwareHTTPClientHasBoundedTransportWithoutGlobalTimeout(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(context.Background(), nil, nil, 0)
	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0 for streaming-compatible client", client.Timeout)
	}
	transport := requireHTTPTransport(t, client.Transport)
	if transport.DialContext == nil {
		t.Fatal("transport.DialContext is nil")
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Fatal("TLSHandshakeTimeout is not bounded")
	}
	if transport.ResponseHeaderTimeout <= 0 {
		t.Fatal("ResponseHeaderTimeout is not bounded")
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatal("IdleConnTimeout is not bounded")
	}
	if transport.TLSHandshakeTimeout > time.Minute || transport.ResponseHeaderTimeout > time.Minute {
		t.Fatalf("transport timeouts too large: TLS=%v ResponseHeader=%v", transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout)
	}
}

func TestNewProxyAwareHTTPClientRequestProxyOverridesAuthAndGlobal(t *testing.T) {
	t.Parallel()

	ctx := coreexecutor.WithRequestProxyURL(context.Background(), "http://request-proxy.example:8081")
	client := NewProxyAwareHTTPClient(
		ctx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"},
		0,
	)
	// The fork wraps proxy transports in idleTimeoutRoundTripper, so unwrap
	// before asserting on *http.Transport.
	transport := requireHTTPTransport(t, client.Transport)
	if transport.Proxy == nil {
		t.Fatalf("transport = %#v, want request proxy", client.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
	if errReq != nil {
		t.Fatalf("request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example:8081" {
		t.Fatalf("proxy URL = %v, want request proxy", proxyURL)
	}

	refreshCtx := coreexecutor.WithoutRequestProxyURL(ctx)
	refreshClient := NewProxyAwareHTTPClient(
		refreshCtx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"},
		0,
	)
	refreshTransport := requireHTTPTransport(t, refreshClient.Transport)
	if refreshTransport.Proxy == nil {
		t.Fatalf("refresh transport = %#v, want auth proxy", refreshClient.Transport)
	}
	refreshProxy, errRefresh := refreshTransport.Proxy(req)
	if errRefresh != nil {
		t.Fatalf("refresh proxy: %v", errRefresh)
	}
	if refreshProxy == nil || refreshProxy.String() != "http://auth-proxy.example:8080" {
		t.Fatalf("refresh proxy URL = %v, want auth proxy", refreshProxy)
	}
}

func TestNewStandardHTTPClientPreservesProxyPriorityAndNoResponseDeadline(t *testing.T) {
	ctx := coreexecutor.WithRequestProxyURL(t.Context(), "http://request-proxy.example:8081")
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example:8080"}}
	auth := &cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"}
	client := NewStandardHTTPClient(ctx, cfg, auth)
	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %s, want no total timeout", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("transport = %T, want standard transport without response deadline", client.Transport)
	}
	req, err := http.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example:8081" {
		t.Fatalf("proxy = %v, want request override", proxyURL)
	}
}

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport := requireHTTPTransport(t, client.Transport)
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewProxyAwareHTTPClientDefaultHasTransportDeadlinesWithoutTotalTimeout(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(context.Background(), nil, nil, 0)

	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0 so streaming responses are not capped by total duration", client.Timeout)
	}
	transport := requireHTTPTransport(t, client.Transport)
	if transport.ResponseHeaderTimeout <= 0 {
		t.Fatal("ResponseHeaderTimeout is not configured")
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Fatal("TLSHandshakeTimeout is not configured")
	}
}

func TestIdleTimeoutBodyClosesWhenReadStalls(t *testing.T) {
	t.Parallel()

	inner := &blockingReadCloser{closed: make(chan struct{})}
	body := newIdleTimeoutBody(inner, 25*time.Millisecond)
	defer func() { _ = body.Close() }()

	done := make(chan error, 1)
	go func() {
		_, err := body.Read(make([]byte, 1))
		done <- err
	}()

	select {
	case <-inner.closed:
	case <-time.After(time.Second):
		t.Fatal("idle timeout body did not close stalled reader")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Read() error = nil, want close error")
		}
	case <-time.After(time.Second):
		t.Fatal("Read() did not return after idle close")
	}
}

func TestIdleTimeoutBodyAllowsActiveReads(t *testing.T) {
	t.Parallel()

	body := newIdleTimeoutBody(io.NopCloser(strings.NewReader("ok")), time.Minute)
	defer func() { _ = body.Close() }()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("ReadAll() = %q, want ok", string(got))
	}
}

type blockingReadCloser struct {
	closed chan struct{}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	<-r.closed
	return 0, http.ErrAbortHandler
}

func (r *blockingReadCloser) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}

func requireHTTPTransport(t *testing.T, rt http.RoundTripper) *http.Transport {
	t.Helper()
	if wrapper, ok := rt.(idleTimeoutRoundTripper); ok {
		rt = wrapper.next
	}
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", rt)
	}
	return transport
}

func TestProxyConnectionPoolReuse(t *testing.T) {
	for _, tc := range []struct {
		name            string
		enabled         bool
		wantConnections int
	}{{"disabled", false, 2}, {"enabled", true, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			ResetProxyTransports()
			t.Cleanup(ResetProxyTransports)
			var connections atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			cfg := testConfig()
			auth := &cliproxyauth.Auth{ProxyURL: "direct"}
			if tc.enabled {
				auth.ProxyConnectionPool = poolEnabled()
			}
			for i := 0; i < 2; i++ {
				client := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 0)
				resp, err := client.Get(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(io.Discard, resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if err = resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if !tc.enabled {
					client.CloseIdleConnections()
				}
			}
			if got := connections.Load(); got != int32(tc.wantConnections) {
				t.Fatalf("connections = %d, want %d", got, tc.wantConnections)
			}
		})
	}
}

func TestProxyConnectionPoolSettingsIsolation(t *testing.T) {
	enabled := true
	maxIdle := 4
	cfg := testConfig()
	auth := poolAuth("settings", &cliproxyauth.ProxyConnectionPoolOverride{Enabled: &enabled, IdleConnTimeout: "15s", MaxIdleConnsPerHost: &maxIdle})
	defer ResetProxyTransports()
	first := pooledProxyTransport("direct", cfg, auth)
	if first == nil || first.IdleConnTimeout != 15*time.Second || first.MaxIdleConnsPerHost != 4 {
		t.Fatalf("unexpected transport: %+v", first)
	}
	if pooledProxyTransport("direct", cfg, auth) != first {
		t.Fatal("transport not reused")
	}
	auth.ProxyConnectionPool.IdleConnTimeout = "20s"
	if pooledProxyTransport("direct", cfg, auth) == first {
		t.Fatal("different settings reused transport")
	}
	if pooledProxyTransport("http://localhost:1234", cfg, auth) == first {
		t.Fatal("different proxies reused transport")
	}
	ResetProxyTransports()
	if pooledProxyTransport("direct", cfg, auth) == first {
		t.Fatal("reset did not remove transport")
	}
}

func TestProxyConnectionPoolAuthenticatedProxyIsolation(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	cfg := testConfig()
	auth := poolOptInAuth("credential-url")

	first := pooledProxyTransport("http://user:old-secret@localhost:1234", cfg, auth)
	second := pooledProxyTransport("http://user:old-secret@localhost:1234", cfg, auth)
	if first == nil || second != first {
		t.Fatal("identical authenticated proxy must reuse its pool")
	}
	rotated := pooledProxyTransport("http://user:new-secret@localhost:1234", cfg, auth)
	if rotated == nil || rotated == first {
		t.Fatal("different proxy credentials on the same host must not share a pool")
	}
	socks := pooledProxyTransport("socks5://user:old-secret@localhost:1234", cfg, auth)
	if socks == nil || socks == first || pooledProxyTransport("socks5://user:old-secret@localhost:1234", cfg, auth) != socks {
		t.Fatal("authenticated socks5 proxy must pool per scheme and credential")
	}
	setting, err := proxyutil.Parse("http://user:old-secret@localhost:1234")
	if err != nil {
		t.Fatal(err)
	}
	cacheKey, label := proxyPoolCacheTarget(setting)
	if strings.Contains(cacheKey, "old-secret") || strings.Contains(label, "old-secret") {
		t.Fatalf("cache target leaked proxy secret: key=%q label=%q", cacheKey, label)
	}
}
func TestProxyConnectionPoolCustomDefaultTransportDeclinesPooling(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	oldDefault := http.DefaultTransport
	defer func() { http.DefaultTransport = oldDefault }()

	customRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200}, nil
	})
	http.DefaultTransport = customRT

	cfg := testConfig()
	auth := poolOptInAuth("custom-default")

	// With empty proxy-url, pooling must be declined because http.DefaultTransport
	// cannot be cloned into *http.Transport.
	client := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 0)
	if client.Transport != nil {
		t.Fatalf("client.Transport = %T, want nil so client uses http.DefaultTransport", client.Transport)
	}
}

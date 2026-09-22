package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

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

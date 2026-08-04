package helps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type claudeCodeTLSFingerprintFixture struct {
	ClientHelloLength   int
	JA3                 string
	JA3MD5              string
	ALPN                []string
	HTTPVersion         string
	CipherSuites        []uint16
	ExtensionTypes      []uint16
	ExtensionLengths    [][2]int
	SupportedGroups     []uint16
	PointFormats        []uint8
	SignatureAlgorithms []uint16
	SupportedVersions   []uint16
	KeyShareGroups      []uint16
}

func TestClaudeCodeTLSClientHelloSpecMatches220Capture(t *testing.T) {
	t.Parallel()

	fixture := claudeCodeTLSFingerprintFixture{
		ClientHelloLength: 508,
		JA3:               "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-21,29-23-24,0",
		JA3MD5:            "d871d02cecbde59abbf8f4806134addf",
		ALPN:              []string{"http/1.1"},
		HTTPVersion:       "HTTP/1.1",
		CipherSuites:      []uint16{4865, 4866, 4867, 49195, 49199, 49196, 49200, 52393, 52392, 49161, 49171, 49162, 49172, 156, 157, 47, 53},
		ExtensionTypes:    []uint16{0, 23, 65281, 10, 11, 35, 16, 5, 13, 18, 51, 45, 43, 21},
		ExtensionLengths: [][2]int{
			{0, 22}, {23, 0}, {65281, 1}, {10, 8}, {11, 2}, {35, 0}, {16, 11},
			{5, 5}, {13, 20}, {18, 0}, {51, 38}, {45, 2}, {43, 5}, {21, 231},
		},
		SupportedGroups:     []uint16{29, 23, 24},
		PointFormats:        []uint8{0},
		SignatureAlgorithms: []uint16{1027, 2052, 1025, 1283, 2053, 1281, 2054, 1537, 513},
		SupportedVersions:   []uint16{772, 771},
		KeyShareGroups:      []uint16{29},
	}

	record := captureClaudeCodeClientHello(t)
	if got := len(record) - 9; got != fixture.ClientHelloLength {
		t.Fatalf("ClientHello length = %d, want %d", got, fixture.ClientHelloLength)
	}
	if got := parseClientHelloExtensionLengths(t, record); !reflect.DeepEqual(got, fixture.ExtensionLengths) {
		t.Fatalf("extension lengths = %v, want %v", got, fixture.ExtensionLengths)
	}

	spec, errFingerprint := (&tls.Fingerprinter{}).FingerprintClientHello(record)
	if errFingerprint != nil {
		t.Fatal(errFingerprint)
	}
	actual := summarizeClaudeCodeClientHelloSpec(t, spec)
	if !reflect.DeepEqual(actual.CipherSuites, fixture.CipherSuites) {
		t.Fatalf("cipher suites = %v, want %v", actual.CipherSuites, fixture.CipherSuites)
	}
	if !reflect.DeepEqual(actual.ExtensionTypes, fixture.ExtensionTypes) {
		t.Fatalf("extension types = %v, want %v", actual.ExtensionTypes, fixture.ExtensionTypes)
	}
	if !reflect.DeepEqual(actual.ALPN, fixture.ALPN) {
		t.Fatalf("ALPN = %v, want %v", actual.ALPN, fixture.ALPN)
	}
	if !reflect.DeepEqual(actual.SupportedGroups, fixture.SupportedGroups) {
		t.Fatalf("supported groups = %v, want %v", actual.SupportedGroups, fixture.SupportedGroups)
	}
	if !reflect.DeepEqual(actual.PointFormats, fixture.PointFormats) {
		t.Fatalf("point formats = %v, want %v", actual.PointFormats, fixture.PointFormats)
	}
	if !reflect.DeepEqual(actual.SignatureAlgorithms, fixture.SignatureAlgorithms) {
		t.Fatalf("signature algorithms = %v, want %v", actual.SignatureAlgorithms, fixture.SignatureAlgorithms)
	}
	if !reflect.DeepEqual(actual.SupportedVersions, fixture.SupportedVersions) {
		t.Fatalf("supported versions = %v, want %v", actual.SupportedVersions, fixture.SupportedVersions)
	}
	if !reflect.DeepEqual(actual.KeyShareGroups, fixture.KeyShareGroups) {
		t.Fatalf("key share groups = %v, want %v", actual.KeyShareGroups, fixture.KeyShareGroups)
	}
	if actual.JA3 != fixture.JA3 || actual.JA3MD5 != fixture.JA3MD5 {
		t.Fatalf("JA3 = %q (%s), want %q (%s)", actual.JA3, actual.JA3MD5, fixture.JA3, fixture.JA3MD5)
	}

	transport, ok := newClaudeCodeRoundTripper("").(*http.Transport)
	if !ok {
		t.Fatalf("Claude Code transport type = %T, want *http.Transport", newClaudeCodeRoundTripper(""))
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("Claude Code transport must not force HTTP/2")
	}
	if fixture.HTTPVersion != "HTTP/1.1" {
		t.Fatalf("fixture HTTP version = %q, want HTTP/1.1", fixture.HTTPVersion)
	}
}

func TestClaudeCodeTLSResumptionIsWireSafe(t *testing.T) {
	t.Parallel()

	// RFC 8446 4.2.11 requires pre_shared_key to be the final extension, after
	// the padding extension.
	spec := claudeCodeTLSClientHelloSpec()
	last := spec.Extensions[len(spec.Extensions)-1]
	if _, ok := last.(*tls.UtlsPreSharedKeyExtension); !ok {
		t.Fatalf("last inference extension = %T, want *tls.UtlsPreSharedKeyExtension", last)
	}
	if _, ok := spec.Extensions[len(spec.Extensions)-2].(*tls.UtlsPaddingExtension); !ok {
		t.Fatalf("extension before pre_shared_key = %T, want *tls.UtlsPaddingExtension", spec.Extensions[len(spec.Extensions)-2])
	}

	// Without OmitEmptyPsk uTLS refuses to marshal an empty PSK, and without
	// PreferSkipResumptionOnNilExtension a HelloCustom resumption attempt panics.
	cfg := newClaudeCodeTLSConfig("api.anthropic.com", tls.NewLRUClientSessionCache(claudeCodeSessionCacheCapacity))
	if cfg.ClientSessionCache == nil {
		t.Fatal("ClientSessionCache = nil, want a session cache so resumption is possible")
	}
	if !cfg.OmitEmptyPsk {
		t.Fatal("OmitEmptyPsk = false, want true so an unresumed ClientHello stays byte-identical")
	}
	if !cfg.PreferSkipResumptionOnNilExtension {
		t.Fatal("PreferSkipResumptionOnNilExtension = false, want true to avoid a HelloCustom resumption panic")
	}
}

func TestClaudeCodeRequestHeaderOrderMatchesNative220Capture(t *testing.T) {
	t.Parallel()

	if got, want := claudeCodeRequestHeaderOrder(http.MethodPost, "/v1/messages?beta=true"), claudeCodeMessagesHeaderOrder; !reflect.DeepEqual(got, want) {
		t.Fatalf("Messages header order = %v, want %v", got, want)
	}
	if got, want := claudeCodeRequestHeaderOrder(http.MethodPost, "/v1/messages/count_tokens?beta=true"), claudeCodeCountTokensHeaderOrder; !reflect.DeepEqual(got, want) {
		t.Fatalf("count_tokens header order = %v, want %v", got, want)
	}
	for _, name := range claudeCodeCountTokensHeaderOrder {
		if name == "X-Stainless-Timeout" {
			t.Fatal("count_tokens header order unexpectedly contains X-Stainless-Timeout")
		}
	}
}

func TestCachedClaudeCodeRoundTripperReusesTransport(t *testing.T) {
	t.Parallel()

	const proxyURL = "http://127.0.0.1:29653"
	first := cachedClaudeCodeRoundTripper(proxyURL)
	second := cachedClaudeCodeRoundTripper(proxyURL)
	if first != second {
		t.Fatal("Claude Code transport cache returned different transports for one proxy")
	}
}

func TestCachedClaudeCodeRoundTripperBoundsProxyCardinality(t *testing.T) {
	firstProxy := fmt.Sprintf("http://127.0.0.1:%d", 30000)
	first := cachedClaudeCodeRoundTripper(firstProxy)
	for index := 1; index <= claudeCodeRoundTripperCacheCapacity; index++ {
		cachedClaudeCodeRoundTripper(fmt.Sprintf("http://127.0.0.1:%d", 30000+index))
	}
	if got := claudeCodeRoundTripperCache.Len(); got > claudeCodeRoundTripperCacheCapacity {
		t.Fatalf("transport cache entries = %d, want at most %d", got, claudeCodeRoundTripperCacheCapacity)
	}
	if recreated := cachedClaudeCodeRoundTripper(firstProxy); recreated == first {
		t.Fatal("least recently used proxy transport was not evicted")
	}
}

func TestClaudeCodeTLSClientHelloCapture(t *testing.T) {
	proxyURL := os.Getenv("CPA_TLS_FP_PROXY")
	if proxyURL == "" {
		t.Skip("CPA_TLS_FP_PROXY is not set")
	}

	client := NewUtlsHTTPClient(t.Context(), nil, &cliproxyauth.Auth{ProxyURL: proxyURL}, 0)
	req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewBufferString(`{"model":"claude-opus-4-6","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "dummy-tls-fingerprint")
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
}

func TestFallbackRoundTripperSelectsProviderFingerprint(t *testing.T) {
	t.Parallel()

	route := func(label string) http.RoundTripper {
		return utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"X-Test-Route": []string{label}},
				Body:       io.NopCloser(strings.NewReader("{}")),
				Request:    req,
			}, nil
		})
	}
	roundTripper := &fallbackRoundTripper{
		anthropic: route("anthropic"),
		chrome:    route("chrome"),
		fallback:  route("fallback"),
	}
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "Anthropic HTTPS", url: "https://api.anthropic.com/v1/messages", want: "anthropic"},
		{name: "Anthropic explicit HTTPS port", url: "https://api.anthropic.com:443/v1/messages", want: "anthropic"},
		{name: "Anthropic custom port", url: "https://api.anthropic.com:8443/v1/messages", want: "fallback"},
		{name: "Anthropic userinfo", url: "https://caller@api.anthropic.com/v1/messages", want: "fallback"},
		{name: "Anthropic lookalike", url: "https://api.anthropic.com.example/v1/messages", want: "fallback"},
		{name: "ChatGPT HTTPS", url: "https://chatgpt.com/backend-api/codex/responses", want: "chrome"},
		{name: "Other HTTPS", url: "https://example.com/v1/messages", want: "fallback"},
		{name: "Anthropic HTTP", url: "http://api.anthropic.com/v1/messages", want: "fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, errRequest := http.NewRequest(http.MethodGet, tt.url, nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			resp, errRoundTrip := roundTripper.RoundTrip(req)
			if errRoundTrip != nil {
				t.Fatal(errRoundTrip)
			}
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					t.Errorf("close response body: %v", errClose)
				}
			}()
			if got := resp.Header.Get("X-Test-Route"); got != tt.want {
				t.Fatalf("route = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewUtlsHTTPClientUsesContextRoundTripperForProtectedHost(t *testing.T) {
	t.Parallel()

	for _, targetURL := range []string{
		"https://api.anthropic.com/v1/messages",
		"https://chatgpt.com/backend-api/codex/responses",
	} {
		t.Run(targetURL, func(t *testing.T) {
			called := false
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				called = true
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("{}")),
					Request:    req,
				}, nil
			}))

			client := NewUtlsHTTPClient(ctx, nil, nil, 0)
			resp, err := client.Get(targetURL)
			if err != nil {
				t.Fatalf("client.Get returned error: %v", err)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatalf("response body close returned error: %v", errClose)
			}
			if !called {
				t.Fatal("expected context RoundTripper to handle protected host request")
			}
		})
	}
}

type blockingUtlsDialer struct {
	started chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (d blockingUtlsDialer) Dial(network, addr string) (net.Conn, error) {
	d.once.Do(func() { close(d.started) })
	<-d.release
	return nil, errors.New("released blocking dial")
}

func (d blockingUtlsDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.once.Do(func() { close(d.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.release:
		return nil, errors.New("released blocking dial")
	}
}

func TestUtlsRoundTripperCancelsConnectionSetupWhenRequestContextEnds(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	rt := newUtlsRoundTripper("")
	rt.dialer = blockingUtlsDialer{started: started, release: release, once: &sync.Once{}}
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	if errReq != nil {
		t.Fatalf("NewRequestWithContext returned error: %v", errReq)
	}

	done := make(chan error, 1)
	go func() {
		_, errRoundTrip := rt.RoundTrip(req)
		done <- errRoundTrip
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("utls dial did not start")
	}
	select {
	case errRoundTrip := <-done:
		if !errors.Is(errRoundTrip, context.DeadlineExceeded) {
			t.Fatalf("RoundTrip error = %v, want context deadline exceeded", errRoundTrip)
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTrip did not return after request context deadline")
	}
}

type stalledUtlsHandshakeDialer struct {
	started chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (d stalledUtlsHandshakeDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d stalledUtlsHandshakeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	clientConn, serverConn := net.Pipe()
	d.once.Do(func() { close(d.started) })
	go func() {
		select {
		case <-ctx.Done():
		case <-d.release:
		}
		_ = serverConn.Close()
	}()
	return clientConn, nil
}

func TestUtlsRoundTripperCancelsConnectionSetupWhenTLSHandshakeStalls(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	rt := newUtlsRoundTripper("")
	rt.dialer = stalledUtlsHandshakeDialer{started: started, release: release, once: &sync.Once{}}
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	if errReq != nil {
		t.Fatalf("NewRequestWithContext returned error: %v", errReq)
	}

	done := make(chan error, 1)
	go func() {
		_, errRoundTrip := rt.RoundTrip(req)
		done <- errRoundTrip
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("utls dial did not start")
	}
	select {
	case errRoundTrip := <-done:
		if !errors.Is(errRoundTrip, context.DeadlineExceeded) {
			t.Fatalf("RoundTrip error = %v, want context deadline exceeded", errRoundTrip)
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTrip did not return after request context deadline")
	}
}

func TestUtlsRoundTripperCancelsSameHostWaitWhenRequestContextEnds(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	rt := newUtlsRoundTripper("")
	rt.dialer = blockingUtlsDialer{started: started, release: release, once: &sync.Once{}}

	firstReq, errReq := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	if errReq != nil {
		t.Fatalf("NewRequestWithContext returned error: %v", errReq)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, errRoundTrip := rt.RoundTrip(firstReq)
		firstDone <- errRoundTrip
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first utls dial did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	waitingReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/codex/second", nil)
	if errReq != nil {
		t.Fatalf("NewRequestWithContext returned error: %v", errReq)
	}

	waitingDone := make(chan error, 1)
	go func() {
		_, errRoundTrip := rt.RoundTrip(waitingReq)
		waitingDone <- errRoundTrip
	}()

	select {
	case errRoundTrip := <-waitingDone:
		if !errors.Is(errRoundTrip, context.DeadlineExceeded) {
			t.Fatalf("RoundTrip error = %v, want context deadline exceeded", errRoundTrip)
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTrip did not return while waiting on same-host connection")
	}

	close(release)
	<-firstDone
}

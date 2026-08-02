package claude

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

type claudeRefreshHandshakeTimeoutContextKey struct{}

var claudeOAuthRefreshHeaderOrder = []string{
	"Accept",
	"Content-Type",
	"User-Agent",
	"Content-Length",
	"Accept-Encoding",
	"Host",
	"Connection",
}

var claudeOAuthProfileHeaderOrder = []string{
	"Accept",
	"Content-Type",
	"Authorization",
	"Cache-Control",
	"User-Agent",
	"Accept-Encoding",
	"Host",
	"Connection",
}

func claudeOAuthRequestHeaderOrder(method, requestTarget string) []string {
	if method == http.MethodGet && strings.HasPrefix(requestTarget, "/api/oauth/profile") {
		return claudeOAuthProfileHeaderOrder
	}
	return claudeOAuthRefreshHeaderOrder
}

// claudeOAuthTLSClientHelloSpec reproduces the compact Node/OpenSSL profile
// Claude Code 2.1.220 uses for Axios OAuth control-plane requests. Unlike the
// inference profile, it advertises no ALPN extension and therefore uses
// HTTP/1.1 without negotiating a protocol.
func claudeOAuthTLSClientHelloSpec() *tls.ClientHelloSpec {
	return &tls.ClientHelloSpec{
		TLSVersMin:         tls.VersionTLS12,
		TLSVersMax:         tls.VersionTLS13,
		CompressionMethods: []uint8{0},
		CipherSuites: []uint16{
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		Extensions: []tls.TLSExtension{
			&tls.SNIExtension{},
			&tls.ExtendedMasterSecretExtension{},
			&tls.RenegotiationInfoExtension{Renegotiation: tls.RenegotiateOnceAsClient},
			&tls.SupportedCurvesExtension{Curves: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}},
			&tls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&tls.SessionTicketExtension{},
			&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []tls.SignatureScheme{
				tls.ECDSAWithP256AndSHA256,
				tls.PSSWithSHA256,
				tls.PKCS1WithSHA256,
				tls.ECDSAWithP384AndSHA384,
				tls.PSSWithSHA384,
				tls.PKCS1WithSHA384,
				tls.PSSWithSHA512,
				tls.PKCS1WithSHA512,
				tls.PKCS1WithSHA1,
			}},
			&tls.KeyShareExtension{KeyShares: []tls.KeyShare{{Group: tls.X25519}}},
			&tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}},
			&tls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},
		},
	}
}

// utlsRoundTripper uses Claude Code's OAuth control-plane TLS and HTTP/1.1
// profile while retaining net/http proxy, cancellation, response parsing and
// connection lifecycle semantics.
type utlsRoundTripper struct {
	dialer    proxy.Dialer
	transport *http.Transport
}

func newUtlsRoundTripper(cfg *config.SDKConfig) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if cfg != nil {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(cfg.ProxyURL)
		if errBuild != nil {
			log.Errorf("failed to configure proxy dialer for %q: %v", proxyutil.Redact(cfg.ProxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}

	roundTripper := &utlsRoundTripper{dialer: dialer}
	roundTripper.transport = &http.Transport{
		ForceAttemptHTTP2: false,
		DialTLSContext:    roundTripper.dialTLSContext,
	}
	return roundTripper
}

func (t *utlsRoundTripper) dialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var (
		conn net.Conn
		err  error
	)
	if contextDialer, ok := t.dialer.(proxy.ContextDialer); ok {
		conn, err = contextDialer.DialContext(ctx, network, addr)
	} else {
		conn, err = t.dialer.Dial(network, addr)
	}
	if err != nil {
		return nil, fmt.Errorf("claude oauth tls: dial upstream: %w", err)
	}

	host, _, errSplit := net.SplitHostPort(addr)
	if errSplit != nil {
		if errClose := conn.Close(); errClose != nil {
			log.Debugf("claude oauth tls: close failed connection: %v", errClose)
		}
		return nil, fmt.Errorf("claude oauth tls: split upstream address: %w", errSplit)
	}
	tlsConn := tls.UClient(conn, &tls.Config{ServerName: host}, tls.HelloCustom)
	if errPreset := tlsConn.ApplyPreset(claudeOAuthTLSClientHelloSpec()); errPreset != nil {
		if errClose := tlsConn.Close(); errClose != nil {
			log.Debugf("claude oauth tls: close connection after preset failure: %v", errClose)
		}
		return nil, fmt.Errorf("claude oauth tls: apply ClientHello: %w", errPreset)
	}
	handshakeCtx := ctx
	if handshakeTimeout, _ := ctx.Value(claudeRefreshHandshakeTimeoutContextKey{}).(time.Duration); handshakeTimeout > 0 {
		var cancelHandshake context.CancelFunc
		handshakeCtx, cancelHandshake = context.WithTimeout(ctx, handshakeTimeout)
		defer cancelHandshake()
	}
	if errHandshake := tlsConn.HandshakeContext(handshakeCtx); errHandshake != nil {
		if errClose := tlsConn.Close(); errClose != nil {
			log.Debugf("claude oauth tls: close connection after handshake failure: %v", errClose)
		}
		return nil, fmt.Errorf("claude oauth tls: handshake upstream: %w", errHandshake)
	}
	return httpwire.NewOrderedRequestConn(tlsConn, claudeOAuthRequestHeaderOrder), nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.transport.RoundTrip(req)
}

func (t *utlsRoundTripper) CloseIdleConnections() {
	t.transport.CloseIdleConnections()
}

func NewAnthropicHttpClient(cfg *config.SDKConfig) *http.Client {
	return &http.Client{Transport: newUtlsRoundTripper(cfg)}
}

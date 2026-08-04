package claude

import (
	"net/http"
	"strings"

	tls "github.com/refraction-networking/utls"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
)

var claudeOAuthRefreshHeaderOrder = []string{
	"Accept", "Content-Type", "User-Agent", "Content-Length",
	"Accept-Encoding", "Host", "Connection",
}

var claudeOAuthInspectHeaderOrder = []string{
	"Accept", "Content-Type", "Authorization", "Cache-Control",
	"User-Agent", "Accept-Encoding", "Host", "Connection",
}

var claudeOAuthInspectTargets = []string{
	"/api/oauth/profile",
	"/api/oauth/claude_cli/roles",
}

func claudeOAuthRequestHeaderOrder(method, requestTarget string) []string {
	if method == http.MethodGet {
		for _, target := range claudeOAuthInspectTargets {
			if strings.HasPrefix(requestTarget, target) {
				return claudeOAuthInspectHeaderOrder
			}
		}
	}
	return claudeOAuthRefreshHeaderOrder
}

const (
	claudeOAuthSessionCacheCapacity      = 8
	claudeOAuthProxySessionCacheCapacity = 64
)

var claudeOAuthSessionCaches = internalcache.NewBoundedLRU[string, tls.ClientSessionCache](
	claudeOAuthProxySessionCacheCapacity,
	nil,
)

func claudeOAuthSessionCache(proxyURL string) tls.ClientSessionCache {
	return claudeOAuthSessionCaches.GetOrAdd(proxyURL, func() tls.ClientSessionCache {
		return tls.NewLRUClientSessionCache(claudeOAuthSessionCacheCapacity)
	})
}

func newClaudeOAuthTLSConfig(host string, sessionCache tls.ClientSessionCache) *tls.Config {
	return &tls.Config{
		ServerName:                         host,
		ClientSessionCache:                 sessionCache,
		OmitEmptyPsk:                       true,
		PreferSkipResumptionOnNilExtension: true,
	}
}

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
			&tls.UtlsPreSharedKeyExtension{},
		},
	}
}

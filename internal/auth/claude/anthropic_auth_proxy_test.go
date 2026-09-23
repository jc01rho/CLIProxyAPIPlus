package claude

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestNewClaudeAuthWithProxyURL_OverrideDirectTakesPrecedence(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "socks5://proxy.example.com:1080"}}
	auth := NewClaudeAuthWithProxyURL(cfg, "direct")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected standard transport, got %T", auth.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatalf("expected direct transport, got proxy function %p", transport.Proxy)
	}
}

func TestNewClaudeAuthWithProxyURL_OverrideProxyAppliedWithoutConfig(t *testing.T) {
	auth := NewClaudeAuthWithProxyURL(nil, "socks5://proxy.example.com:1080")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected standard transport, got %T", auth.httpClient.Transport)
	}
	if transport.DialContext == nil || transport.Proxy != nil {
		t.Fatalf("expected SOCKS5 dialer without HTTP proxy, got %#v", transport)
	}
}

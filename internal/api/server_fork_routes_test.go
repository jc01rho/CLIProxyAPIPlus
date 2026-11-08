package api

import (
	"net/http"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// TestForkOnlyRoutesRegistered guards fork-only public routes that upstream
// merges have silently dropped before (e.g. /v1/systemone lost in the v8 merge).
func TestForkOnlyRoutesRegistered(t *testing.T) {
	// Management routes only register when a management secret exists, so the
	// DeepAI assertions below need a server that actually enables them.
	cfg := &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}}}
	cfg.RemoteManagement.SecretKey = "test-management-key"
	server := newTestServerWithConfig(t, cfg)

	want := []struct{ method, path string }{
		{http.MethodPost, "/v1/systemone"},
		{http.MethodGet, "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"},
		{http.MethodGet, "/v1beta/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"},
		// Fork-only DeepAI provider management surface. These routes are the
		// only way the Management Center can read or edit deepai-api-key.
		{http.MethodGet, "/v0/management/deepai-api-key"},
		{http.MethodPut, "/v0/management/deepai-api-key"},
		{http.MethodPatch, "/v0/management/deepai-api-key"},
		{http.MethodDelete, "/v0/management/deepai-api-key"},
	}
	registered := make(map[string]bool)
	for _, route := range server.engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, r := range want {
		if !registered[r.method+" "+r.path] {
			t.Errorf("fork-only route %s %s is not registered", r.method, r.path)
		}
	}
}

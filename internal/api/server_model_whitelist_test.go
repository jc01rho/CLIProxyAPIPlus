package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func newModelWhitelistTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"whitelist-key", "another-key"},
		},
	}
	cfg.APIKeyModelWhitelists = map[string][]string{
		"whitelist-key": {"gpt-6*", "jev-*"},
	}
	return newTestServerWithConfig(t, cfg)
}

func serveWhitelistRequest(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer whitelist-key")
	server.engine.ServeHTTP(recorder, request)
	return recorder
}

func TestServerEnforcesAPIKeyModelWhitelistOnChatCompletions(t *testing.T) {
	server := newModelWhitelistTestServer(t)

	tests := []struct {
		name      string
		model     string
		forbidden bool
	}{
		{name: "allowed model passes the whitelist", model: "gpt-6.1"},
		{name: "requested model outside the whitelist is rejected", model: "cc-opus-5.5", forbidden: true},
		{name: "fallback target outside the whitelist is rejected", model: "deepseek-v4.1-flash", forbidden: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := `{"model":"` + test.model + `","messages":[{"role":"user","content":"hi"}]}`
			recorder := serveWhitelistRequest(t, server, http.MethodPost, "/v1/chat/completions", body)
			if test.forbidden {
				if recorder.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want %d (body = %s)", recorder.Code, http.StatusForbidden, recorder.Body.String())
				}
				if !strings.Contains(recorder.Body.String(), "model_not_allowed") {
					t.Fatalf("body = %s, want model_not_allowed", recorder.Body.String())
				}
				return
			}
			if recorder.Code == http.StatusForbidden {
				t.Fatalf("allowed model %q was rejected: %s", test.model, recorder.Body.String())
			}
		})
	}
}

func TestServerEnforcesAPIKeyModelWhitelistOnGeminiRoutes(t *testing.T) {
	server := newModelWhitelistTestServer(t)

	recorder := serveWhitelistRequest(t, server, http.MethodPost, "/v1beta/models/cc-opus-5.5:generateContent", `{"contents":[]}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body = %s)", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestServerLeavesUnlistedAPIKeyUnrestricted(t *testing.T) {
	server := newModelWhitelistTestServer(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"cc-opus-5.5","messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer another-key")
	server.engine.ServeHTTP(recorder, request)

	if recorder.Code == http.StatusForbidden {
		t.Fatalf("key without a whitelist was restricted: %s", recorder.Body.String())
	}
}

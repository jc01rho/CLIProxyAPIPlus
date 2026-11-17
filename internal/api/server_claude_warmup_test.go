package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClaudeWarmupRunRouteIsRegisteredAndProtected(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)

	for _, path := range []string{"/v0/management/claude-warmup/run", "/v8/management/claude-warmup/run"} {
		registered := false
		for _, route := range server.engine.Routes() {
			if route.Method == http.MethodPost && route.Path == path {
				registered = true
			}
		}
		if !registered {
			t.Fatalf("claude warmup run route %s not registered", path)
		}

		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated run on %s: status %d, want 401", path, w.Code)
		}
	}
}

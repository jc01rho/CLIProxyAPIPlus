package antigravity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// assertAgyCLIQuotaIdentity checks the identity quota and model-discovery RPCs
// send: the agy CLI harness identity cortexkit/antigravity-auth quota-manager.ts
// uses for fetchAvailableModels and retrieveUserQuotaSummary.
func assertAgyCLIQuotaIdentity(t *testing.T, label, userAgent string) {
	t.Helper()
	if strings.Contains(userAgent, "GeminiCLI/") {
		t.Fatalf("%s used the Gemini CLI identity: %s", label, userAgent)
	}
	if !strings.HasPrefix(userAgent, "antigravity/cli/1.5.8 (aidev_client; os_type=") ||
		!strings.HasSuffix(userAgent, "; cl=1020628950; auth_method=consumer)") {
		t.Fatalf("%s User-Agent = %q, want the agy CLI 1.5.8 identity", label, userAgent)
	}
}

func TestQuotaRequestsUseAgyCLIIdentity(t *testing.T) {
	seen := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = append(seen[r.URL.Path], r.Header.Get("User-Agent"))
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "retrieveUserQuotaSummary") {
			_, _ = w.Write([]byte(`{"groups":[{"displayName":"Gemini Models","buckets":[{"window":"weekly","remainingFraction":0.5}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"buckets":[]}`))
	}))
	defer server.Close()

	if _, err := FetchUserQuotaSummary(context.Background(), server.Client(), "token", "proj-1", server.URL); err != nil {
		t.Fatalf("FetchUserQuotaSummary: %v", err)
	}
	if _, err := FetchAvailableModels(context.Background(), server.Client(), "token", "proj-1", server.URL); err != nil {
		t.Fatalf("FetchAvailableModels: %v", err)
	}

	for _, path := range []string{"/v1internal:retrieveUserQuotaSummary", "/v1internal:fetchAvailableModels"} {
		agents := seen[path]
		if len(agents) == 0 {
			t.Fatalf("%s was never called", path)
		}
		for _, ua := range agents {
			assertAgyCLIQuotaIdentity(t, path, ua)
		}
	}
}

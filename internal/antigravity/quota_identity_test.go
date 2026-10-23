package antigravity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// quotaIdentityFloorMajor/Minor is the Cloud Code client version below which the
// upstream hides the "Gemini Models" quota group and the newest model ids. Measured
// 2026-10-16 on daily-cloudcode-pa.googleapis.com: antigravity/cli/1.1.6 -> Claude/GPT
// group only, antigravity/cli/2.23.0 and antigravity/hub/2.23.0 -> both groups.
const (
	quotaIdentityFloorMajor = 2
	quotaIdentityFloorMinor = 23
)

func antigravityIdentityVersion(t *testing.T, userAgent string) (int, int) {
	t.Helper()
	rest := strings.TrimPrefix(userAgent, "antigravity/")
	rest = strings.TrimPrefix(rest, "hub/")
	rest = strings.TrimPrefix(rest, "cli/")
	if idx := strings.IndexAny(rest, " (;\t"); idx >= 0 {
		rest = rest[:idx]
	}
	parts := strings.SplitN(strings.TrimSpace(rest), ".", 3)
	if len(parts) < 2 {
		t.Fatalf("User-Agent %q has no major.minor version", userAgent)
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		t.Fatalf("User-Agent %q version unparsable: %v %v", userAgent, errMajor, errMinor)
	}
	return major, minor
}

func assertIdentityMeetsQuotaFloor(t *testing.T, label, userAgent string) {
	t.Helper()
	if strings.Contains(userAgent, "GeminiCLI/") {
		t.Fatalf("%s used the Gemini CLI identity: %s", label, userAgent)
	}
	if !strings.HasPrefix(userAgent, "antigravity/") {
		t.Fatalf("%s User-Agent = %q, want an antigravity/* identity", label, userAgent)
	}
	major, minor := antigravityIdentityVersion(t, userAgent)
	if major < quotaIdentityFloorMajor || (major == quotaIdentityFloorMajor && minor < quotaIdentityFloorMinor) {
		t.Fatalf("%s User-Agent = %q is below the %d.%d Cloud Code floor, so the Gemini quota group and newest models are hidden",
			label, userAgent, quotaIdentityFloorMajor, quotaIdentityFloorMinor)
	}
}

// TestQuotaRequestsUseIdentityAboveTheGeminiQuotaFloor pins the identity used by the
// quota and model-discovery RPCs: the legacy agy harness identity silently drops the
// Gemini Models group upstream, so these calls must use the executor runtime identity.
func TestQuotaRequestsUseIdentityAboveTheGeminiQuotaFloor(t *testing.T) {
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
			assertIdentityMeetsQuotaFloor(t, path, ua)
		}
	}
}

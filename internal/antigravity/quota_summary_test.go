package antigravity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchUserQuotaSummaryFallsBackPastForbiddenAndParsesBuckets(t *testing.T) {
	const fixture = `{
		"groups": [
			{
				"displayName": "Gemini Models",
				"description": "Models within this group: Gemini Flash, Gemini Pro",
				"buckets": [
					{
						"bucketId": "gemini-weekly",
						"displayName": "Weekly Limit Remaining",
						"window": "weekly",
						"resetTime": "2026-09-04T01:27:21Z",
						"remainingFraction": 0.25
					}
				]
			}
		]
	}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1internal:retrieveUserQuotaSummary" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "antigravity/cli/") {
			t.Errorf("User-Agent = %q, want antigravity/cli/ harness", got)
		}
		if strings.Contains(r.Header.Get("User-Agent"), "GeminiCLI/") {
			t.Errorf("User-Agent used the Gemini CLI form: %s", r.Header.Get("User-Agent"))
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
		}
		if !strings.Contains(string(body), `"project":"proj-1"`) {
			t.Errorf("body = %s", body)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"subscription"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()

	summary, err := FetchUserQuotaSummary(context.Background(), server.Client(), "token", "proj-1", server.URL, server.URL)
	if err != nil {
		t.Fatalf("FetchUserQuotaSummary: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (403 then 200)", calls)
	}
	if len(summary.Groups) != 1 || summary.Groups[0].DisplayName != "Gemini Models" {
		t.Fatalf("groups = %#v", summary.Groups)
	}
	bucket := summary.Groups[0].Buckets[0]
	if bucket.BucketID != "gemini-weekly" || bucket.Window != "weekly" || bucket.RemainingFraction == nil || *bucket.RemainingFraction != 0.25 {
		t.Fatalf("bucket = %#v", bucket)
	}
}

func TestFetchUserQuotaSummaryStopsOnClientError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad project"}`))
	}))
	defer server.Close()

	_, err := FetchUserQuotaSummary(context.Background(), server.Client(), "token", "proj-1", server.URL, server.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (400 does not fall through)", calls)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %v", err)
	}
}

package kiro

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGetUsageLimitsMatchesKiroCLI2191Contract(t *testing.T) {
	t.Parallel()

	auth := &KiroAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", req.Method)
		}
		if got, want := req.URL.Scheme+"://"+req.URL.Host+req.URL.Path, "https://management.eu-west-1.kiro.dev/"; got != want {
			t.Fatalf("URL = %q, want %q", got, want)
		}
		if got := req.URL.Query().Get("profileArn"); got != BuilderIDRequestProfileARN {
			t.Fatalf("profileArn query = %q", got)
		}
		if got := req.Header.Get("X-Amz-Target"); got != "AmazonCodeWhispererService.GetUsageLimits" {
			t.Fatalf("X-Amz-Target = %q", got)
		}
		var body map[string]string
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body["profileArn"] != BuilderIDRequestProfileARN || body["resourceType"] != "AGENTIC_REQUEST" {
			t.Fatalf("body = %#v", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"subscriptionInfo":{"subscriptionTitle":"Builder ID"},
				"usageBreakdownList":[
					{"resourceType":"CODE_LINES","currentUsageWithPrecision":999,"usageLimitWithPrecision":999},
					{"resourceType":"AGENTIC_REQUEST","currentUsageWithPrecision":12.5,"usageLimitWithPrecision":50}
				],
				"nextDateReset":12345
			}`)),
		}, nil
	})}}

	usage, err := auth.GetUsageLimits(t.Context(), &KiroTokenData{
		AccessToken: "token",
		AuthMethod:  "builder-id",
		APIRegion:   "eu-west-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.CurrentUsage != 12.5 || usage.UsageLimit != 50 {
		t.Fatalf("usage = %+v", usage)
	}
}

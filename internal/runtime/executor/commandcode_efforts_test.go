package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func Test_CommandCodeParsedProfileEfforts(t *testing.T) {
	cases := []struct {
		name string
		page string
		want []string
	}{
		{
			name: "plain ladder",
			page: "Reasoning efforts low, medium, high are supported;",
			want: []string{"low", "medium", "high"},
		},
		{
			name: "with remap",
			page: "Reasoning efforts low, xhigh, max are supported; xhigh maps to max.",
			want: []string{"low", "max"},
		},
		{
			name: "no ladder",
			page: "This model has no adjustable reasoning effort.",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commandCodeParsedProfileEfforts(tc.page)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("parsed efforts = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parsed efforts = %v, want %v", got, tc.want)
			}
			for _, w := range tc.want {
				if !commandCodeEffortSupported(got, w) {
					t.Fatalf("parsed efforts %v missing %q", got, w)
				}
			}
		})
	}
}

func Test_CommandCodeIsReasoningEffortRejection(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"error":"unsupported effort"}`, true},
		{422, `{"error":"invalid reasoning_effort"}`, true},
		{400, `{"error":"reasoning effort not allowed"}`, true},
		{400, `{"error":"bad request"}`, false},
		{500, `{"error":"unsupported effort"}`, false},
		{200, `ok`, false},
	}
	for _, tc := range cases {
		if got := commandCodeIsReasoningEffortRejection(tc.status, tc.body); got != tc.want {
			t.Errorf("status=%d body=%q: got %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func Test_CommandCodeStripReasoningEffort(t *testing.T) {
	payload := []byte(`{"params":{"model":"m","reasoning_effort":"high","stream":true}}`)
	stripped := commandCodeStripReasoningEffort(payload)
	if stripped == nil {
		t.Fatal("strip returned nil for a payload with reasoning_effort")
	}
	if commandCodePayloadEffort(stripped) != "" {
		t.Fatalf("reasoning_effort still present after strip: %s", stripped)
	}
	if commandCodePayloadModel(stripped) != "m" {
		t.Fatalf("model lost after strip: %s", stripped)
	}
	// A payload without the field returns nil.
	if got := commandCodeStripReasoningEffort([]byte(`{"params":{"model":"m"}}`)); got != nil {
		t.Fatalf("strip returned %s for a payload without reasoning_effort", got)
	}
}

func Test_CommandCodeRefreshReasoningEfforts_updates_table(t *testing.T) {
	commandCodeResetRefreshedEffortsForTest()
	defer commandCodeResetRefreshedEffortsForTest()

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", commandCodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://commandcode.ai/models/deepseek-v4-flash" {
			t.Fatalf("profile URL = %q, want deepseek-v4-flash profile", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("Reasoning efforts low, medium, high are supported;")),
		}, nil
	}))

	efforts := commandCodeRefreshReasoningEfforts(ctx, &config.Config{}, &cliproxyauth.Auth{}, "deepseek/deepseek-v4-flash")
	if efforts == nil {
		t.Fatal("refresh returned nil")
	}
	if !commandCodeEffortSupported(efforts, "low") || !commandCodeEffortSupported(efforts, "high") {
		t.Fatalf("refreshed efforts = %v, want low/high from the profile", efforts)
	}
	// The refreshed ladder now wins over the static table.
	if got := commandCodeReasoningEfforts("deepseek/deepseek-v4-flash"); !commandCodeEffortSupported(got, "low") {
		t.Fatalf("reasoning efforts after refresh = %v, want the refreshed ladder", got)
	}
}

func Test_CommandCodeEffortRejectionRetryPayload(t *testing.T) {
	commandCodeResetRefreshedEffortsForTest()
	defer commandCodeResetRefreshedEffortsForTest()

	// The profile page documents a ladder that no longer includes "high", so a
	// 422 rejection of "high" warrants a retry without the field.
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", commandCodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("Reasoning efforts low, max are supported;")),
		}, nil
	}))

	payload := []byte(`{"params":{"model":"deepseek/deepseek-v4-flash","reasoning_effort":"high","stream":true}}`)
	retry := commandCodeEffortRejectionRetryPayload(ctx, &config.Config{}, &cliproxyauth.Auth{}, payload, http.StatusUnprocessableEntity, `{"error":"unsupported effort"}`)
	if retry == nil {
		t.Fatal("expected a retry payload for a stale effort rejection")
	}
	if commandCodePayloadEffort(retry) != "" {
		t.Fatalf("retry payload still carries reasoning_effort: %s", retry)
	}

	// A non-effort rejection never retries.
	if got := commandCodeEffortRejectionRetryPayload(ctx, &config.Config{}, &cliproxyauth.Auth{}, payload, http.StatusBadRequest, `{"error":"bad request"}`); got != nil {
		t.Fatalf("non-effort rejection returned a retry payload: %s", got)
	}
	// A rejection whose refreshed ladder still supports the effort never retries.
	commandCodeResetRefreshedEffortsForTest()
	ctx2 := context.WithValue(context.Background(), "cliproxy.roundtripper", commandCodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("Reasoning efforts high, max are supported;")),
		}, nil
	}))
	if got := commandCodeEffortRejectionRetryPayload(ctx2, &config.Config{}, &cliproxyauth.Auth{}, payload, http.StatusUnprocessableEntity, `{"error":"unsupported effort"}`); got != nil {
		t.Fatalf("supported-effort rejection returned a retry payload: %s", got)
	}
}

func Test_CommandCodeEffortRejectionRetryPayload_unknown_model(t *testing.T) {
	commandCodeResetRefreshedEffortsForTest()
	defer commandCodeResetRefreshedEffortsForTest()

	payload := []byte(`{"params":{"model":"unknown/model","reasoning_effort":"high","stream":true}}`)
	// No profile URL for an unknown model → no refresh → no retry.
	if got := commandCodeEffortRejectionRetryPayload(context.Background(), &config.Config{}, &cliproxyauth.Auth{}, payload, http.StatusUnprocessableEntity, `{"error":"unsupported effort"}`); got != nil {
		t.Fatalf("unknown model returned a retry payload: %s", got)
	}
}

package zcode

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestTransientPollErrorClassification verifies the broker poll error policy
// matches the extension reference (oauth.ts pollCliDeviceFlowOnce): 408/429/5xx
// and transport failures keep polling; any other 4xx fails the flow.
func TestTransientPollErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"transport error", errors.New("dial tcp: connection refused"), true},
		{"decode error", fmt.Errorf("zcode broker.cli.poll: decode response: %w", errors.New("unexpected EOF")), true},
		{"408 request timeout", &BrokerHTTPError{StatusCode: http.StatusRequestTimeout, Label: "broker.cli.poll"}, true},
		{"429 too many requests", &BrokerHTTPError{StatusCode: http.StatusTooManyRequests, Label: "broker.cli.poll"}, true},
		{"500 server error", &BrokerHTTPError{StatusCode: http.StatusInternalServerError, Label: "broker.cli.poll"}, true},
		{"503 unavailable", &BrokerHTTPError{StatusCode: http.StatusServiceUnavailable, Label: "broker.cli.poll"}, true},
		{"400 bad request", &BrokerHTTPError{StatusCode: http.StatusBadRequest, Label: "broker.cli.poll"}, false},
		{"401 unauthorized", &BrokerHTTPError{StatusCode: http.StatusUnauthorized, Label: "broker.cli.poll"}, false},
		{"404 unknown flow", &BrokerHTTPError{StatusCode: http.StatusNotFound, Label: "broker.cli.poll"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TransientPollError(tc.err); got != tc.want {
				t.Errorf("TransientPollError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestBrokerHTTPErrorMasksBody verifies the error message never leaks an
// unredacted token from the broker response body.
func TestBrokerHTTPErrorMasksBody(t *testing.T) {
	err := &BrokerHTTPError{
		StatusCode: http.StatusBadRequest,
		Label:      "broker.cli.poll",
		Body:       redactSecrets(`{"error":"bad token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk"}`),
	}
	if got := err.Error(); got == "" {
		t.Fatal("Error() = empty")
	} else if _, leaked := findJWT(got); leaked {
		t.Errorf("Error() leaked a token: %s", got)
	}
}

// TestCLIPollResultReady verifies completion is keyed on the token, not on the
// status label (extension oauth.ts treats a present token as completion).
func TestCLIPollResultReady(t *testing.T) {
	cases := []struct {
		name   string
		result *CLIPollResult
		want   bool
	}{
		{"nil", nil, false},
		{"pending without token", &CLIPollResult{Status: "pending"}, false},
		{"token without status", &CLIPollResult{ZaiAccessToken: "access"}, true},
		{"ready with token", &CLIPollResult{Status: "ready", ZaiAccessToken: "access"}, true},
		{"blank token", &CLIPollResult{Status: "ready", ZaiAccessToken: "   "}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.result.Ready(); got != tc.want {
				t.Errorf("Ready() = %v, want %v", got, tc.want)
			}
		})
	}
}

// findJWT reports whether text still contains a JWT-shaped run.
func findJWT(text string) (string, bool) {
	match := jwtPattern.FindString(text)
	return match, match != ""
}

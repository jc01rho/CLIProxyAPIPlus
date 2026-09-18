package executor

// Ported behavior from kiro-lb's kiro/account_errors.py (classify_error) and
// kiro/kiro_errors.py (SUSPENSION_REASON / _SUSPENSION_MARKERS), AGPL-3.0,
// minpeter/jc01rho fork of jwadow/kiro-gateway.

import (
	"net/http"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClassifyKiroUpstreamError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		want       kiroErrorDecision
	}{
		{"402 no body", 402, nil, KiroErrorRecoverable},
		{"402 monthly quota reason", 402, []byte(`{"reason":"MONTHLY_REQUEST_COUNT"}`), KiroErrorRecoverable},
		{"403 no body", 403, nil, KiroErrorRecoverable},
		{"403 with message", 403, []byte(`{"message":"token expired"}`), KiroErrorRecoverable},
		{"429 no body", 429, nil, KiroErrorRecoverable},
		{"429 with body", 429, []byte(`{"message":"rate limited"}`), KiroErrorRecoverable},
		{"400 monthly request count", 400, []byte(`{"reason":"MONTHLY_REQUEST_COUNT"}`), KiroErrorRecoverable},
		{"400 invalid model id", 400, []byte(`{"reason":"INVALID_MODEL_ID"}`), KiroErrorRecoverable},
		{"400 content length exceeds threshold", 400, []byte(`{"reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`), KiroErrorFatal},
		{"400 no reason", 400, []byte(`{"message":"Improperly formed request."}`), KiroErrorFatal},
		{"400 unknown reason", 400, []byte(`{"reason":"SOME_OTHER_REASON"}`), KiroErrorFatal},
		{"400 empty body", 400, nil, KiroErrorFatal},
		{"422", 422, nil, KiroErrorFatal},
		{"422 with body", 422, []byte(`{"message":"validation failed"}`), KiroErrorFatal},
		{"502", 502, nil, KiroErrorRecoverable},
		{"503", 503, nil, KiroErrorRecoverable},
		{"504", 504, nil, KiroErrorRecoverable},
		{"500", 500, nil, KiroErrorFatal},
		{"501 other 5xx", 501, nil, KiroErrorFatal},
		{"599 other 5xx", 599, nil, KiroErrorFatal},
		{"unknown status 418", 418, nil, KiroErrorFatal},
		{"unknown status 0", 0, nil, KiroErrorFatal},
		{"unknown status 200 (should never route here but must not panic)", 200, nil, KiroErrorFatal},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyKiroUpstreamError(tc.statusCode, tc.body)
			if got != tc.want {
				t.Errorf("classifyKiroUpstreamError(%d, %q) = %v, want %v", tc.statusCode, tc.body, got, tc.want)
			}
		})
	}
}

func TestKiroErrorReasonFromBody(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"clean json reason", []byte(`{"reason":"MONTHLY_REQUEST_COUNT"}`), "MONTHLY_REQUEST_COUNT"},
		{"clean json reason lowercase", []byte(`{"reason":"invalid_model_id"}`), "INVALID_MODEL_ID"},
		{"json with message and reason", []byte(`{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`), "CONTENT_LENGTH_EXCEEDS_THRESHOLD"},
		{"no reason field, message only", []byte(`{"message":"Improperly formed request."}`), ""},
		{"empty body", nil, ""},
		{"not json but contains marker substring", []byte(`some wrapper text MONTHLY_REQUEST_COUNT trailing`), "MONTHLY_REQUEST_COUNT"},
		{"not json contains invalid model id marker", []byte(`upstream said INVALID_MODEL_ID oops`), "INVALID_MODEL_ID"},
		{"not json contains content length marker", []byte(`error: CONTENT_LENGTH_EXCEEDS_THRESHOLD`), "CONTENT_LENGTH_EXCEEDS_THRESHOLD"},
		{"not json, no known marker", []byte(`totally unrelated text`), ""},
		{"malformed json falls back to substring scan", []byte(`{"reason": MONTHLY_REQUEST_COUNT malformed`), "MONTHLY_REQUEST_COUNT"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := kiroErrorReasonFromBody(tc.body)
			if got != tc.want {
				t.Errorf("kiroErrorReasonFromBody(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestIsKiroSuspendedBody(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{"empty body", nil, false},
		{"bare SUSPENDED token (legacy behavior)", []byte(`{"message":"Account SUSPENDED"}`), true},
		{"reason TEMPORARILY_SUSPENDED", []byte(`{"reason":"TEMPORARILY_SUSPENDED"}`), true},
		{"reason lowercase temporarily_suspended still matches uppercase scan", []byte(`{"reason":"temporarily_suspended"}`), true},
		{"marker: temporarily suspended", []byte(`{"message":"Your account has been temporarily suspended."}`), true},
		{"marker: temporarily is suspended", []byte(`{"message":"Your account temporarily is suspended due to abuse."}`), true},
		{"marker: locked your account", []byte(`{"message":"We locked your account for suspicious activity."}`), true},
		{"marker: locked it as a", []byte(`{"message":"We locked it as a precaution."}`), true},
		{"unrelated 403 message", []byte(`{"message":"token expired"}`), false},
		{"unrelated text no markers", []byte(`just some random error text`), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isKiroSuspendedBody(tc.body)
			if got != tc.want {
				t.Errorf("isKiroSuspendedBody(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestKiroMonthlyQuotaCooldownUsesReportedResetWithinBounds(t *testing.T) {
	now := parseTestTime(t, "2026-09-17T00:00:00Z")
	tests := []struct {
		name    string
		auth    *cliproxyauth.Auth
		body    []byte
		headers http.Header
		want    time.Duration
	}{
		{name: "unknown reset uses one hour floor", want: time.Hour},
		{
			name: "body reset waits through reset margin",
			body: []byte(`{"reason":"MONTHLY_REQUEST_COUNT","nextDateReset":1789862400000}`),
			want: 3*24*time.Hour + 5*time.Minute,
		},
		{
			name: "quota signal reset waits past old six hour window",
			auth: &cliproxyauth.Auth{Quota: cliproxyauth.QuotaState{Signals: map[string]string{
				"monthly_quota_reset_at": "2026-09-19T00:00:00Z",
			}}},
			want: 2*24*time.Hour + 5*time.Minute,
		},
		{
			name: "past reset is bounded by floor",
			body: []byte(`{"resetAt":"2026-09-01T00:00:00Z"}`),
			want: time.Hour,
		},
		{
			name: "distant reset is bounded by ceiling",
			body: []byte(`{"resetAt":"2027-09-17T00:00:00Z"}`),
			want: 7 * 24 * time.Hour,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := kiroMonthlyQuotaCooldown(test.auth, test.body, test.headers, now); got != test.want {
				t.Fatalf("cooldown = %v, want %v", got, test.want)
			}
		})
	}
}

func TestKiroRetryAfterFromHeader(t *testing.T) {
	now := parseTestTime(t, "2026-09-17T00:00:00Z")

	tests := []struct {
		name    string
		raw     string
		wantNil bool
		want    string // expected duration string, only checked when !wantNil
	}{
		{"empty header", "", true, ""},
		{"delta seconds", "120", false, "2m0s"},
		{"zero seconds", "0", false, "0s"},
		{"negative seconds invalid", "-5", true, ""},
		{"not a number or date", "not-a-value", true, ""},
		{"http-date in the future", "Thu, 17 Sep 2026 00:05:00 GMT", false, "5m0s"},
		{"http-date in the past clamps to zero", "Thu, 17 Sep 2026 00:00:00 GMT", false, "0s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := kiroRetryAfterFromHeader(tc.raw, now)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("kiroRetryAfterFromHeader(%q) = %v, want nil", tc.raw, *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("kiroRetryAfterFromHeader(%q) = nil, want %s", tc.raw, tc.want)
			}
			if got.String() != tc.want {
				t.Fatalf("kiroRetryAfterFromHeader(%q) = %s, want %s", tc.raw, got.String(), tc.want)
			}
		})
	}
}

// parseTestTime is a small helper to avoid repeating time.Parse boilerplate
// in the table cases above.
func parseTestTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("failed to parse test time %q: %v", s, err)
	}
	return parsed
}

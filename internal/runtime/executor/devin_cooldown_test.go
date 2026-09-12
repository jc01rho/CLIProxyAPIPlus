package executor

import (
	"net/http"
	"strings"
	"testing"
)

// TestDevinIntermittentErrorIsRequestScoped pins that an intermittent upstream
// rejection maps to a request-shape status.
//
// The credential cooldown path skips request-shape faults. Without this the
// single devin credential was taken out of rotation by one rejection, and the
// requests that followed reported auth_unavailable instead of reaching devin:
// in one sample window 4 real rejections produced 14 auth_unavailable turns.
func TestDevinIntermittentErrorIsRequestScoped(t *testing.T) {
	trailer := []byte(`{"error":{"code":"invalid_argument","message":"an internal error occurred (trace ID: abc)"}}`)
	err := devinStreamFailure(trailer, nil, 0)
	if err == nil {
		t.Fatal("expected a failure")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error does not carry a status: %v", err)
	}
	if status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status.StatusCode(), http.StatusBadRequest)
	}
}

// TestDevinCredentialErrorsStillCool pins that genuine credential faults keep
// cooling the credential: permission_denied means the account cannot use the
// model, which retrying cannot fix.
func TestDevinCredentialErrorsStillCool(t *testing.T) {
	for _, code := range []string{"permission_denied", "unauthenticated", "resource_exhausted"} {
		trailer := []byte(`{"error":{"code":"` + code + `","message":"nope"}}`)
		err := devinStreamFailure(trailer, nil, 0)
		if err == nil {
			t.Fatalf("%s: expected a failure", code)
		}
		if _, ok := err.(interface{ StatusCode() int }); ok {
			t.Fatalf("%s must not be reclassified as a request fault", code)
		}
	}
}

// TestDevinTrailerMessagePreserved pins that the upstream message and trace id
// survive classification.
func TestDevinTrailerMessagePreserved(t *testing.T) {
	trailer := []byte(`{"error":{"code":"invalid_argument","message":"an internal error occurred (trace ID: t1)"}}`)
	err := devinStreamFailure(trailer, nil, 0)
	if err == nil || !strings.Contains(err.Error(), "trace ID: t1") {
		t.Fatalf("trace id lost: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_argument") {
		t.Fatalf("code lost: %v", err)
	}
}

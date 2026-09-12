package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestDevinStreamFailureSurfacesTrailer pins that a Connect trailer carrying
// an error becomes a real error instead of a clean close. A silently empty
// stream reaches the client as a missing terminal event, whose wording
// differs per client format and hides the rejection reason.
func TestDevinStreamFailureSurfacesTrailer(t *testing.T) {
	trailer := []byte(`{"error":{"code":"invalid_argument","message":"an internal error occurred"}}`)
	err := devinStreamFailure(trailer, nil, 3)
	if err == nil {
		t.Fatal("trailer error must surface even when data frames arrived")
	}
	if !strings.Contains(err.Error(), "invalid_argument") {
		t.Fatalf("error = %v", err)
	}
}

// TestDevinStreamFailureEmptyStream pins that a stream with no data frames is
// reported as a failure rather than an empty success.
func TestDevinStreamFailureEmptyStream(t *testing.T) {
	if err := devinStreamFailure(nil, nil, 0); err == nil {
		t.Fatal("a stream with no data frames must fail")
	}
}

// TestDevinStreamFailureCleanClose pins that a healthy stream is unaffected.
func TestDevinStreamFailureCleanClose(t *testing.T) {
	if err := devinStreamFailure([]byte("{}"), nil, 5); err != nil {
		t.Fatalf("clean close reported as failure: %v", err)
	}
	if err := devinStreamFailure(nil, context.Canceled, 5); err != nil {
		t.Fatalf("client cancellation must not be a failure: %v", err)
	}
}

// TestDevinStreamFailureTransportError pins transport failures propagate.
func TestDevinStreamFailureTransportError(t *testing.T) {
	err := devinStreamFailure(nil, errors.New("connection reset"), 2)
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("error = %v", err)
	}
}

// TestDevinTrailerError pins trailer parsing.
func TestDevinTrailerError(t *testing.T) {
	if got := devinTrailerError([]byte("{}")); got != "" {
		t.Fatalf("clean trailer = %q", got)
	}
	if got := devinTrailerError(nil); got != "" {
		t.Fatalf("empty trailer = %q", got)
	}
	got := devinTrailerError([]byte(`{"error":{"code":"invalid_argument","message":"boom"}}`))
	if got != "invalid_argument: boom" {
		t.Fatalf("trailer = %q", got)
	}
}

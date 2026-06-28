package oauthform

import (
	"strings"
	"testing"
)

func TestEncodePreservesOrderAndEscapes(t *testing.T) {
	got := Encode(
		Pair{Key: "b key", Value: "two words"},
		Pair{Key: "a/key", Value: "slash/value"},
	)
	want := "b+key=two+words&a%2Fkey=slash%2Fvalue"
	if got != want {
		t.Fatalf("Encode() = %q, want %q", got, want)
	}
}

func TestMaskSensitivePreservesOrderAndMasksCredentials(t *testing.T) {
	got := MaskSensitive("grant_type=refresh_token&refresh_token=secret-refresh&client_secret=secret-client&code_verifier=secret-verifier&error=invalid_grant")
	for _, leaked := range []string{"secret-refresh", "secret-client", "secret-verifier"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("MaskSensitive leaked %q in %q", leaked, got)
		}
	}
	want := "grant_type=refresh_token&refresh_token=secr...resh&client_secret=secr...ient&code_verifier=secr...fier&error=invalid_grant"
	if got != want {
		t.Fatalf("MaskSensitive() = %q, want %q", got, want)
	}
}

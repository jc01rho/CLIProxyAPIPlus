package helps

import (
	"strings"
	"testing"
)

// cursorCallIDTestCase fixtures exercise the opencodex codec semantics exactly: CR/LF
// content wins the primary namespace, reserved ids are re-encoded under the escape
// namespace, and any malformed payload round-trips unchanged rather than corrupting pairing.
type cursorCallIDTestCase struct {
	name string
	id   string
}

func TestEncodeCursorCallID_PassThrough(t *testing.T) {
	cases := []cursorCallIDTestCase{
		{name: "simple id", id: "call_abc123_1"},
		{name: "uuid", id: "550e8400-e29b-41d4-a716-446655440000"},
		{name: "empty", id: ""},
		{name: "spaces only", id: "   "},
		{name: "tab only", id: "\t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EncodeCursorCallID(tc.id)
			if got != tc.id {
				t.Fatalf("pass-through expected %q, got %q", tc.id, got)
			}
			// Decode of pass-through must also yield the original.
			if back := DecodeCursorCallID(got); back != tc.id {
				t.Fatalf("decode expected %q, got %q", tc.id, back)
			}
		})
	}
}

func TestEncodeCursorCallID_Newline(t *testing.T) {
	id := "call_abc123_1\nfc_def456_2"
	enc := EncodeCursorCallID(id)
	if enc == id {
		t.Fatalf("id with CR/LF must be encoded, got unchanged %q", enc)
	}
	if strings.ContainsAny(enc, "\n\r") {
		t.Fatalf("encoded id must not contain CR/LF, got %q", enc)
	}
	if !strings.HasPrefix(enc, cursorCallIDPrefix) {
		t.Fatalf("encoded newline id must use primary prefix %q, got %q", cursorCallIDPrefix, enc)
	}
	if back := DecodeCursorCallID(enc); back != id {
		t.Fatalf("decode round-trip: want %q, got %q", id, back)
	}
}

func TestEncodeCursorCallID_CarriageReturn(t *testing.T) {
	id := "call_abc\r123"
	enc := EncodeCursorCallID(id)
	if strings.ContainsAny(enc, "\n\r") {
		t.Fatalf("encoded CR id must not contain CR/LF, got %q", enc)
	}
	if back := DecodeCursorCallID(enc); back != id {
		t.Fatalf("decode round-trip: want %q, got %q", id, back)
	}
}

func TestEncodeCursorCallID_ReservedEscape(t *testing.T) {
	// An id already sitting in the primary namespace must be re-encoded under the escape
	// namespace so the round-trip stays injective.
	id := cursorCallIDPrefix + "preexisting"
	enc := EncodeCursorCallID(id)
	if enc == id {
		t.Fatalf("reserved id must be re-encoded, got unchanged %q", enc)
	}
	if !strings.HasPrefix(enc, cursorCallIDEscapePrefix) {
		t.Fatalf("reserved escape must use %q, got %q", cursorCallIDEscapePrefix, enc)
	}
	if back := DecodeCursorCallID(enc); back != id {
		t.Fatalf("decode round-trip: want %q, got %q", id, back)
	}
}

func TestDecodeCursorCallID_MalformedPassThrough(t *testing.T) {
	cases := []string{
		cursorCallIDPrefix + "no-separator",
		cursorCallIDPrefix + "two.separators.here",
		cursorCallIDPrefix + "payload.badbase64!@#",
		cursorCallIDPrefix + "payload." + strings.Repeat("a", 64), // tag length wrong
		"ocxc1_unknownprefix_xx",
		"",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if got := DecodeCursorCallID(c); got != c {
				t.Fatalf("malformed payload must pass through, want %q got %q", c, got)
			}
		})
	}
}

func TestDecodeCursorCallID_ForeignProvenanceRejected(t *testing.T) {
	// Simulate a process restart: encode with one key, swap the key, decode should pass through.
	id := "call_xyz\nfc_999"
	enc := EncodeCursorCallID(id)
	ResetCursorCallIDProvenanceForTests()
	if got := DecodeCursorCallID(enc); got != enc {
		t.Fatalf("foreign provenance must not decode, want pass-through %q got %q", enc, got)
	}
}

func TestDecodeCursorCallID_NamespaceGuard(t *testing.T) {
	// ocxc1_ must only decode to content with CR/LF. An attacker who learns a prefix and
	// fabricates a payload without newline must see pass-through, not silent unwrap.
	b64 := "Y2FsbF9hYmM" // base64url("call_abc") - no CR/LF
	enc := cursorCallIDPrefix + b64 + "." + strings.Repeat("A", 22)
	if got := DecodeCursorCallID(enc); got != enc {
		t.Fatalf("namespace guard must reject newline-free payload under primary prefix, want %q got %q", enc, got)
	}
}

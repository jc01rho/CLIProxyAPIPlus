package helps

// Cursor wire call-id codec (ported from opencodex/src/adapters/cursor/call-id.ts).
//
// Cursor's wire delivers tool-call ids that can be two identifiers glued with a literal newline
// ("call-<uuid>-<n>\nfc_<uuid>_<n>"). When such an id leaks into line-oriented consumers
// (logging, splitting, validation) the CR/LF breaks them. The codec encodes ids containing
// CR/LF into a versioned single-line form with a keyed HMAC-SHA256 provenance tag, and decodes
// the same shape back to the exact upstream bytes. Ids already in the codec's reserved namespace
// are re-encoded under a different prefix so the round-trip remains injective.
//
// The process key is intentionally ephemeral: after a process restart the decoder treats every
// previously-encoded value as opaque upstream text and passes it through, rather than guessing
// from attacker- or provider-controlled bytes. This keeps provenance state constant-size.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

const (
	cursorCallIDPrefix       = "ocxc1_"
	cursorCallIDEscapePrefix = "ocxc1e_"
	cursorCallIDDomain       = "opencodex:cursor-call-id:v1\x00"
	cursorCallIDSeparator    = "."
	cursorCallIDTagBytes     = 16
)

// cursorCallIDProvenanceKey is generated lazily on first use. Test code can reset it via
// ResetCursorCallIDProvenanceForTests; production callers never see the reset path.
var cursorCallIDProvenanceKey []byte

func cursorCallIDGetProvenanceKey() []byte {
	if len(cursorCallIDProvenanceKey) == 32 {
		return cursorCallIDProvenanceKey
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// rand.Read on linux uses getrandom(2) and only fails when the kernel is broken.
		// A zero key would still satisfy hmac.Equal (both sides would be the same), but
		// returning a distinct deterministic fallback lets callers detect the failure
		// via the decode round-trip rather than silently leaking insecure provenance.
		for i := range key {
			key[i] = byte(i)
		}
	}
	cursorCallIDProvenanceKey = key
	return cursorCallIDProvenanceKey
}

// ResetCursorCallIDProvenanceForTests re-seeds the provenance key. Test-only.
func ResetCursorCallIDProvenanceForTests() {
	cursorCallIDProvenanceKey = make([]byte, 32)
	_, _ = rand.Read(cursorCallIDProvenanceKey)
}

// cursorCallIDNeedsEncoding reports whether id contains CR/LF (the codec's actual job).
func cursorCallIDNeedsEncoding(id string) bool {
	return strings.ContainsAny(id, "\n\r")
}

// cursorCallIDIsReserved reports whether id already sits in a namespace this codec owns.
func cursorCallIDIsReserved(id string) bool {
	return strings.HasPrefix(id, cursorCallIDPrefix) ||
		strings.HasPrefix(id, cursorCallIDEscapePrefix)
}

func cursorCallIDProvenanceTag(prefix, payload string) []byte {
	h := hmac.New(sha256.New, cursorCallIDGetProvenanceKey())
	h.Write([]byte(cursorCallIDDomain))
	h.Write([]byte(prefix))
	h.Write([]byte(payload))
	sum := h.Sum(nil)
	return sum[:cursorCallIDTagBytes]
}

func cursorCallIDEncodeWithProvenance(prefix, id string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(id))
	tag := base64.RawURLEncoding.EncodeToString(cursorCallIDProvenanceTag(prefix, payload))
	return prefix + payload + cursorCallIDSeparator + tag
}

func cursorCallIDHasValidProvenance(prefix, payload, tag string) bool {
	received, err := base64.RawURLEncoding.DecodeString(tag)
	if err != nil {
		return false
	}
	if len(received) != cursorCallIDTagBytes {
		return false
	}
	return hmac.Equal(received, cursorCallIDProvenanceTag(prefix, payload))
}

// EncodeCursorCallID encodes a Cursor wire call id into a single-line Responses-safe id.
// CR/LF content takes the primary namespace; reserved ids are re-encoded under the escape
// prefix to keep the round-trip injective. Ids that need no encoding pass through unchanged.
func EncodeCursorCallID(id string) string {
	if cursorCallIDNeedsEncoding(id) {
		return cursorCallIDEncodeWithProvenance(cursorCallIDPrefix, id)
	}
	if cursorCallIDIsReserved(id) {
		return cursorCallIDEncodeWithProvenance(cursorCallIDEscapePrefix, id)
	}
	return id
}

// DecodeCursorCallID decodes a Responses-visible call id back to the exact Cursor wire id.
// Non-encoded ids (including legacy raw multi-line ids replayed by older clients) pass through
// unchanged; a malformed encoded payload also passes through rather than corrupting pairing.
func DecodeCursorCallID(id string) string {
	escaped := strings.HasPrefix(id, cursorCallIDEscapePrefix)
	if !escaped && !strings.HasPrefix(id, cursorCallIDPrefix) {
		return id
	}
	prefix := cursorCallIDPrefix
	if escaped {
		prefix = cursorCallIDEscapePrefix
	}
	encoded := id[len(prefix):]
	firstSep := strings.Index(encoded, cursorCallIDSeparator)
	lastSep := strings.LastIndex(encoded, cursorCallIDSeparator)
	if firstSep <= 0 || firstSep != lastSep {
		return id
	}
	payload := encoded[:firstSep]
	tag := encoded[firstSep+len(cursorCallIDSeparator):]
	if !cursorCallIDHasValidProvenance(prefix, payload, tag) {
		return id
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return id
	}
	decodedStr := string(decoded)
	// Round-trip guard: only trust payloads our encoder could have produced.
	if base64.RawURLEncoding.EncodeToString([]byte(decodedStr)) != payload {
		return id
	}
	// Each namespace admits exactly what its encoder puts there.
	if escaped {
		if !cursorCallIDIsReserved(decodedStr) {
			return id
		}
	} else {
		if !cursorCallIDNeedsEncoding(decodedStr) {
			return id
		}
	}
	return decodedStr
}

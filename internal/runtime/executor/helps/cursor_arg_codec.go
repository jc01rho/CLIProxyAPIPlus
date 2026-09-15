package helps

import (
	"encoding/json"
	"strings"
)

// Cursor argument codec helpers (ported from opencodex/src/adapters/cursor/arg-codec.ts).
//
// opencodex's wire delivers tool arguments as `map<string, bytes>` where the bytes hold a
// protobuf ValueSchema encoding. CPA's Chat-Completions path already receives JSON, so the
// immediate use here is JSON-bytes-to-Go-value (the TS source falls back to JSON.parse on
// the UTF-8 bytes when the protobuf decode fails). The protobuf-decoding path is left as a
// future hook for any cursor-wire endpoint that does emit ValueSchema bytes.

// DecodeCursorArgValue decodes a Cursor-wire argument payload into a Go value. It first tries
// strict JSON; on failure it falls back to the raw bytes as a string. This mirrors the TS
// fallback chain in decodeCursorArgValue().
func DecodeCursorArgValue(raw []byte) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&v); err == nil {
		return v
	}
	return string(raw)
}

// DecodeCursorArgsMap decodes every entry of a Cursor-wire `map<string, bytes>` into a
// `map[string]interface{}`. nil/missing args produce an empty map rather than nil so callers
// can range over the result unconditionally.
func DecodeCursorArgsMap(raw map[string][]byte) map[string]interface{} {
	out := make(map[string]interface{}, len(raw))
	for k, v := range raw {
		out[k] = DecodeCursorArgValue(v)
	}
	return out
}

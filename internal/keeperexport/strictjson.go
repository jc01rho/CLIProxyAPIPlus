package keeperexport

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// Wire limits from contract sections 2, 6, and 7.
const (
	// MaxBodyBytes is the decompressed request body maximum.
	MaxBodyBytes = 1048576
	// MaxPayloadBytes is the per-event serialized payload value maximum.
	MaxPayloadBytes = 65536
	// MaxBatchEvents is the usage batch event-count maximum.
	MaxBatchEvents = 500
	// MaxMetadataItems is the metadata snapshot item-count maximum.
	MaxMetadataItems = 5000
	// MaxSafeInteger is the JavaScript safe-integer ceiling for uint64 fields.
	MaxSafeInteger = 9007199254740991

	maxScanDepth = 100
)

// ProtocolVersion is the only accepted keeper-export protocol version.
const ProtocolVersion = "keeper-export/v1"

type scanResult struct {
	bodyInstanceKey bool
}

// scanStrict performs the token-level precheck required by contract section 2.1:
// exactly one top-level object, no duplicate object keys at any depth, valid
// UTF-8, no trailing content, and no control characters in strings. When
// trackInstanceKeys is set it also records whether any object key is an
// instance selector, which is forbidden in public request bodies.
func scanStrict(data []byte, trackInstanceKeys bool) (*scanResult, *Error) {
	res := &scanResult{}
	if len(data) == 0 || !utf8.Valid(data) {
		return nil, protocolError("invalid_json")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, protocolError("invalid_json")
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, protocolError("invalid_json")
	}
	if perr := scanObjectBody(dec, 1, res, trackInstanceKeys); perr != nil {
		return nil, perr
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, protocolError("invalid_json")
	}
	return res, nil
}

func scanObjectBody(dec *json.Decoder, depth int, res *scanResult, trackInstanceKeys bool) *Error {
	if depth > maxScanDepth {
		return protocolError("invalid_json")
	}
	seen := make(map[string]struct{})
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return protocolError("invalid_json")
		}
		key, ok := tok.(string)
		if !ok {
			return protocolError("invalid_json")
		}
		if _, dup := seen[key]; dup {
			return protocolError("invalid_json")
		}
		seen[key] = struct{}{}
		if perr := checkStringChars(key); perr != nil {
			return perr
		}
		if trackInstanceKeys && (key == "instanceId" || key == "instance_id") {
			res.bodyInstanceKey = true
		}
		if perr := scanValue(dec, depth, res, trackInstanceKeys); perr != nil {
			return perr
		}
	}
	if _, err := dec.Token(); err != nil {
		return protocolError("invalid_json")
	}
	return nil
}

func scanValue(dec *json.Decoder, depth int, res *scanResult, trackInstanceKeys bool) *Error {
	if depth+1 > maxScanDepth {
		return protocolError("invalid_json")
	}
	tok, err := dec.Token()
	if err != nil {
		return protocolError("invalid_json")
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return scanObjectBody(dec, depth+1, res, trackInstanceKeys)
		case '[':
			for dec.More() {
				if perr := scanValue(dec, depth+1, res, trackInstanceKeys); perr != nil {
					return perr
				}
			}
			if _, err := dec.Token(); err != nil {
				return protocolError("invalid_json")
			}
			return nil
		default:
			return protocolError("invalid_json")
		}
	case string:
		return checkStringChars(t)
	case json.Number, bool, nil:
		// Token() already validated number syntax; NaN and Infinity never scan.
		return nil
	default:
		return protocolError("invalid_json")
	}
}

// checkStringChars enforces the contract section 2.3 rule that no string may
// contain NUL, ASCII control characters, or DEL.
func checkStringChars(s string) *Error {
	for _, r := range s {
		if r < 0x20 || r == 0x7F {
			return protocolError("invalid_field")
		}
	}
	return nil
}

// decodeTyped decodes an already scanned document into v, rejecting unknown
// fields and type mismatches with their stable codes.
func decodeTyped(data []byte, v interface{}) *Error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return protocolError("unknown_field")
		}
		return protocolError("invalid_field")
	}
	return nil
}

// checkProtocolVersion enforces the exact keeper-export/v1 version string.
// A missing protocolVersion field is a payload-layer missing-required-field
// invalid_field; a present-but-wrong value is unsupported_protocol_version.
func checkProtocolVersion(data []byte) *Error {
	var probe struct {
		ProtocolVersion json.RawMessage `json:"protocolVersion"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return protocolError("invalid_json")
	}
	if len(probe.ProtocolVersion) == 0 || string(probe.ProtocolVersion) == "null" {
		return protocolError("invalid_field")
	}
	var version string
	if err := json.Unmarshal(probe.ProtocolVersion, &version); err != nil {
		return protocolError("invalid_field")
	}
	if version != ProtocolVersion {
		return protocolError("unsupported_protocol_version")
	}
	return nil
}

// requireKeys verifies that a scanned object contains every required key, even
// when the value is JSON null.
func requireKeys(raw json.RawMessage, keys ...string) *Error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return protocolError("invalid_field")
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return protocolError("invalid_field")
		}
	}
	return nil
}

// requestPrecheck applies the shared public-request pipeline: body limit,
// token-level strict scan, body instance-selector ban, and version check.
func requestPrecheck(data []byte) *Error {
	if len(data) > MaxBodyBytes {
		return protocolError("request_too_large")
	}
	scan, perr := scanStrict(data, true)
	if perr != nil {
		return perr
	}
	if scan.bodyInstanceKey {
		return protocolError("body_instance_forbidden")
	}
	return checkProtocolVersion(data)
}

// responsePrecheck applies the shared response pipeline: strict scan and
// version check. Responses legitimately carry instance identity, so the
// instance-selector ban does not apply.
func responsePrecheck(data []byte) *Error {
	if _, perr := scanStrict(data, false); perr != nil {
		return perr
	}
	return checkProtocolVersion(data)
}

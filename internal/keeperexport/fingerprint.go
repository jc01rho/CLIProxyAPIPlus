package keeperexport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// APIKeyFingerprint computes the instance-bound pseudonymous API-key
// fingerprint from contract section 3: HMAC-SHA-256 with the instance
// fingerprint secret as key and the exact raw API-key bytes as message,
// rendered as "akf1_" plus lowercase hex. An empty raw key yields nil (JSON
// null), never an HMAC.
func APIKeyFingerprint(secret, rawKey []byte) *string {
	if len(rawKey) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(rawKey)
	fingerprint := "akf1_" + hex.EncodeToString(mac.Sum(nil))
	return &fingerprint
}

// FingerprintVector is one entry of the shared fingerprint-vectors fixture.
type FingerprintVector struct {
	RawKeyUtf8  string  `json:"rawKeyUtf8"`
	Fingerprint *string `json:"fingerprint"`
}

// FingerprintVectors is the typed cross-repo fingerprint vector object. It is
// not an HTTP body and therefore carries no protocolVersion.
type FingerprintVectors struct {
	FingerprintSecretHex string              `json:"fingerprintSecretHex"`
	Vectors              []FingerprintVector `json:"vectors"`
}

// DecodeFingerprintVectors strictly decodes the shared fingerprint vector
// fixture and validates every expected fingerprint's grammar.
func DecodeFingerprintVectors(data []byte) (*FingerprintVectors, *Error) {
	if _, perr := scanStrict(data, false); perr != nil {
		return nil, perr
	}
	var vectors FingerprintVectors
	if perr := decodeTyped(data, &vectors); perr != nil {
		return nil, perr
	}
	secret, err := hex.DecodeString(vectors.FingerprintSecretHex)
	if err != nil || len(secret) != 32 {
		return nil, protocolError("invalid_field")
	}
	for _, vector := range vectors.Vectors {
		if vector.Fingerprint != nil && !isFingerprint(*vector.Fingerprint) {
			return nil, protocolError("invalid_field")
		}
	}
	return &vectors, nil
}

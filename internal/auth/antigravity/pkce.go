// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
//
// This file implements PKCE (Proof Key for Code Exchange) generation following the
// cortexkit/antigravity-auth reference client, which uses S256 with a 32-byte random verifier.
package antigravity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PKCECodes is a generated PKCE verifier and challenge pair.
type PKCECodes struct {
	CodeVerifier  string
	CodeChallenge string
}

// GeneratePKCECodes generates a fresh PKCE verifier (43-character base64url string
// derived from 32 random bytes) and the corresponding S256 code challenge.
func GeneratePKCECodes() (*PKCECodes, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("antigravity pkce: read random bytes: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return &PKCECodes{CodeVerifier: verifier, CodeChallenge: challenge}, nil
}

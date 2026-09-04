package zcode

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"time"
)

// sha256Sum computes HMAC-SHA256(message, key) for signature vector tests.
func sha256Sum(msg, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

func fixedTime() time.Time { return time.UnixMilli(1740000000000) }

// marshalEd25519PKCS8 serializes an Ed25519 private key in PKCS8 DER.
func marshalEd25519PKCS8(priv ed25519.PrivateKey) ([]byte, error) {
	return x509.MarshalPKCS8PrivateKey(priv)
}

// sealPrivateCipher encrypts a PKCS8 key with the exact handshake layout:
// 12-byte GCM nonce || ciphertext||tag, AAD = apiKeyID.
func sealPrivateCipher(apiKeyID, apiKeySecret string, pkcs8 []byte) (string, error) {
	key, err := hkdf.Key(sha256.New, []byte(apiKeySecret), []byte(SignHKDFSalt), SignInfoPrivateKey, 32)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	// The desktop stores the DER as a base64 *string* in the plaintext
	// (TextDecoder -> atob on the client side), so seal the base64 text.
	encoded := []byte(base64.StdEncoding.EncodeToString(pkcs8))
	sealed := gcm.Seal(nil, nonce, encoded, []byte(apiKeyID))
	return base64.StdEncoding.EncodeToString(append(append([]byte{}, nonce...), sealed...)), nil
}

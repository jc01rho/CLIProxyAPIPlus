// Package zcode — Client Signing V4 (reverse-engineered from ZCode desktop 3.10.2).
//
// The ZCode desktop client authenticates Z.AI coding-plan traffic with a
// two-stage scheme: a one-time "handshake" that exchanges the provisioned
// Z.AI API key for an Ed25519 private key, then per-request signing with an
// Ed25519 signature and an 8-bit proof of work. This file replicates the
// wire-visible algorithms only; no ZCode code is copied.
//
// Constants (observed from the app):
//   - HKDF salt:                "WD_CLIENT_SIGN_KDF_SALT"
//   - handshake HKDF info:      "getSignKey_hmac"
//   - private-key HKDF info:    "ed25519_priv"
//   - handshake message prefix: "get_sign_key"
//   - handshake path:           "/api/paas/c1f3a7e2/v2/client"
//   - app id:                   "zcode"
//   - nonce length:             16 hex chars
//   - PoW bits:                 8
//   - PoW max counter:          2^32 - 1
//
// Handshake:
//
//	POST {origin}/api/paas/c1f3a7e2/v2/client
//	Authorization: Bearer {apiKeyId}.{apiKeySecret}
//	body: {"apiKey":"<id>.<secret>","nonce":"<16hex>","sig":"<b64>","ts":"<ms>"}
//
//	sig = base64(HMAC-SHA256(HKDF-SHA256(secret, salt, "getSignKey_hmac"),
//	                         "get_sign_key\n{id}\n{ts}\n{nonce}"))
//
// Response: {"code":200,"data":{"privateCipher":"<b64>"}}
//
//	privateCipher = base64(AES-256-GCM ciphertext)
//	key     = HKDF-SHA256(secret, salt, "ed25519_priv")
//	iv      = cipher[:12]
//	plaintext = AES-256-GCM-Decrypt(key, iv, cipher[12:], AAD=id)
//	plaintext is UTF-8 base64 → PKCS8 Ed25519 private key.
//
// Per-request headers (signing requires an X-Session-Id header to exist):
//
//	X-Client-Ts      = String(Date.now())  (milliseconds)
//	X-Client-Version = "3.10.2"
//	X-App-Id         = "zcode"
//	X-Client-Nonce   = 16 hex random
//	X-Client-Pow     = 20 hex (12 random hex + 8 counter hex)
//	X-Client-Sig     = base64(Ed25519.Sign(priv,
//	                     "{id}\n{ts}\n{version}\n{sessionId}\n{nonce}"))
package zcode

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client Signing V4 constants observed from the ZCode desktop app.
const (
	// SignHKDFSalt is the fixed HKDF salt used for both handshake and
	// private-key derivation.
	SignHKDFSalt = "WD_CLIENT_SIGN_KDF_SALT"
	// SignInfoHandshake derives the handshake HMAC key.
	SignInfoHandshake = "getSignKey_hmac"
	// SignInfoPrivateKey derives the AES key that wraps the Ed25519 private key.
	SignInfoPrivateKey = "ed25519_priv"
	// SignHandshakePrefix is the first field of the handshake signature message.
	SignHandshakePrefix = "get_sign_key"
	// SignHandshakePath appends to the provider base origin.
	SignHandshakePath = "/api/paas/c1f3a7e2/v2/client"
	// SignAppID is the X-App-Id value.
	SignAppID = "zcode"
	// SignClientVersion is the X-Client-Version value (desktop 3.10.2).
	SignClientVersion = "3.10.2"
	// SignNonceHex is the hex length of handshakes and request nonces.
	SignNonceHex = 16
	// SignPoWBits is the difficulty of the request proof of work.
	SignPoWBits = 8
	// signHandshakeTimeout bounds the handshake HTTP call.
	SignHandshakeTimeout = 10 * time.Second
	// SignPoWMaxCounter is the exclusive upper bound of the PoW counter.
	SignPoWMaxCounter = 1 << 32
)

// signRefreshReasons are the upstream 401 reasons that trigger a handshake
// retry. A second consecutive failure means the credential itself is unusable.
var signRefreshReasons = map[string]bool{
	"VERIFY_SIGNATURE_INVALID": true,
	"VERIFY_APIKEY_EXPIRED":    true,
}

// ParseSigningCredential splits a "{id}.{secret}" Z.AI API key. The split is
// strict: exactly one dot, both sides non-empty.
func ParseSigningCredential(credential string) (apiKeyID, apiKeySecret string, err error) {
	dot := strings.Index(credential, ".")
	if dot <= 0 || dot != strings.LastIndex(credential, ".") {
		return "", "", fmt.Errorf("zcode signing: credential is not \"{id}.{secret}\"")
	}
	id := credential[:dot]
	secret := credential[dot+1:]
	if strings.TrimSpace(id) == "" || strings.TrimSpace(secret) == "" {
		return "", "", fmt.Errorf("zcode signing: credential has an empty id or secret")
	}
	return id, secret, nil
}

// deriveSignBytes runs HKDF-SHA256 over the secret with the fixed salt.
func deriveSignBytes(secret []byte, info string) ([]byte, error) {
	out, err := hkdf.Key(sha256.New, secret, []byte(SignHKDFSalt), info, 32)
	if err != nil {
		return nil, fmt.Errorf("zcode signing: derive %s: %w", info, err)
	}
	return out, nil
}

// HandshakeSignature computes the base64 HMAC signature that authenticates the
// get_sign_key handshake.
func HandshakeSignature(apiKeyID, apiKeySecret, ts, nonce string) (string, error) {
	key, err := deriveSignBytes([]byte(apiKeySecret), SignInfoHandshake)
	if err != nil {
		return "", fmt.Errorf("zcode signing: derive handshake key: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s", SignHandshakePrefix, apiKeyID, ts, nonce)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// PrivateKeyFromCipher decrypts the handshake privateCipher blob into an
// Ed25519 private key. Layout: 12-byte GCM nonce || ciphertext || 16-byte tag,
// authenticated with the apiKeyID as additional data.
func PrivateKeyFromCipher(apiKeyID, apiKeySecret, privateCipher string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(privateCipher)
	if err != nil {
		return nil, fmt.Errorf("zcode signing: privateCipher is not base64: %w", err)
	}
	if len(raw) <= 12+16 {
		return nil, errors.New("zcode signing: privateCipher is too short")
	}
	key, err := deriveSignBytes([]byte(apiKeySecret), SignInfoPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("zcode signing: derive private key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("zcode signing: create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("zcode signing: create GCM: %w", err)
	}
	plain, err := gcm.Open(nil, raw[:12], raw[12:], []byte(apiKeyID))
	if err != nil {
		return nil, fmt.Errorf("zcode signing: decrypt private key: %w", err)
	}
	der, err := base64.StdEncoding.DecodeString(string(plain))
	if err != nil {
		return nil, fmt.Errorf("zcode signing: private key is not base64: %w", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("zcode signing: parse PKCS8 private key: %w", err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("zcode signing: private key is %T, not Ed25519", parsed)
	}
	return priv, nil
}

// SignRequestMessage builds the exact Ed25519 signing message:
//
//	{apiKeyId}\n{ts}\n{clientVersion}\n{sessionId}\n{nonce}
func SignRequestMessage(apiKeyID, ts, clientVersion, sessionID, nonce string) []byte {
	return []byte(apiKeyID + "\n" + ts + "\n" + clientVersion + "\n" + sessionID + "\n" + nonce)
}

// SignRequest returns the base64 Ed25519 signature over the request message.
func SignRequest(priv ed25519.PrivateKey, apiKeyID, ts, clientVersion, sessionID, nonce string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, SignRequestMessage(apiKeyID, ts, clientVersion, sessionID, nonce)))
}

// SolveProofOfWork finds a 20-hex attempt "{12-hex salt}{8-hex counter}" such
// that SHA256("{salt}\n{attempt}") has at least powBits leading zero bits.
// salt = hex(SHA256("{apiKeyId}\n{appId}\n{sessionId}\n{ts}"))[:32].
// With powBits=8 the expected loop count is ~256.
func SolveProofOfWork(apiKeyID, appID, sessionID, ts string, powBits int) (string, error) {
	if powBits < 0 || powBits > 32 {
		return "", fmt.Errorf("zcode signing: powBits must be between 0 and 32, got %d", powBits)
	}
	sum := sha256.Sum256([]byte(apiKeyID + "\n" + appID + "\n" + sessionID + "\n" + ts))
	salt := hexEncode(sum[:])[:32]
	// The desktop calls pi(12): 12 random bytes hex-encoded = 24 hex chars,
	// then appends the 8-hex counter for a 32-char attempt.
	prefix := randomHex(24)
	for counter := 0; counter < SignPoWMaxCounter; counter++ {
		// The desktop formats the counter itself as 8 hex chars
		// (o.toString(16).padStart(8, "0")), not as 8 encoded bytes.
		attempt := prefix + fmt.Sprintf("%08x", counter)
		digest := sha256.Sum256([]byte(salt + "\n" + attempt))
		if leadingZeroBits(digest[:], powBits) {
			return attempt, nil
		}
	}
	return "", errors.New("zcode signing: proof of work unsolvable")
}

// leadingZeroBits reports whether the digest starts with at least n zero bits.
func leadingZeroBits(digest []byte, n int) bool {
	full := n / 8
	for i := 0; i < full; i++ {
		if digest[i] != 0 {
			return false
		}
	}
	rest := n % 8
	if rest == 0 {
		return true
	}
	mask := byte(0xFF << (8 - rest))
	return digest[full]&mask == 0
}

func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0x0F]
	}
	return string(out)
}

func randomHex(n int) string {
	buf := make([]byte, n/2)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n)
	}
	return hexEncode(buf)
}

// Handshake performs the get_sign_key exchange over the given HTTP client and
// returns the Ed25519 private key issued for the credential.
func Handshake(ctx context.Context, baseURL, credential string, httpClient *http.Client) (ed25519.PrivateKey, error) {
	apiKeyID, apiKeySecret, err := ParseSigningCredential(credential)
	if err != nil {
		return nil, err
	}
	handshakeURL, err := url.JoinPath(strings.TrimSuffix(baseURL, "/"), SignHandshakePath)
	if err != nil {
		return nil, fmt.Errorf("zcode signing: build handshake URL: %w", err)
	}
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := randomHex(SignNonceHex)
	sig, err := HandshakeSignature(apiKeyID, apiKeySecret, ts, nonce)
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf(`{"apiKey":%q,"nonce":%q,"sig":%q,"ts":%q}`, credential, nonce, sig, ts)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, handshakeURL, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("zcode signing: build handshake request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zcode signing: handshake request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("zcode signing: read handshake response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zcode signing: handshake http status %d", resp.StatusCode)
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			PrivateCipher string `json:"privateCipher"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("zcode signing: decode handshake response: %w", err)
	}
	if envelope.Code == 500 {
		return nil, errors.New("zcode signing: handshake server error")
	}
	if envelope.Code != 200 {
		return nil, fmt.Errorf("zcode signing: handshake rejected: %s", envelope.Msg)
	}
	if envelope.Data.PrivateCipher == "" {
		return nil, errors.New("zcode signing: handshake omitted privateCipher")
	}
	return PrivateKeyFromCipher(apiKeyID, apiKeySecret, envelope.Data.PrivateCipher)
}

// SigningHeaders is the immutable set of X-Client-* headers for one request.
type SigningHeaders struct {
	Ts      string // X-Client-Ts
	Version string // X-Client-Version
	AppID   string // X-App-Id
	Nonce   string // X-Client-Nonce
	Pow     string // X-Client-Pow
	Sig     string // X-Client-Sig
}

// BuildSigningHeaders derives the per-request signing headers. The caller must
// already carry X-Session-Id on the request.
func BuildSigningHeaders(priv ed25519.PrivateKey, apiKeyID, sessionID, clientVersion string, now time.Time) (map[string]string, error) {
	if sessionID == "" {
		return nil, errors.New("zcode signing: client request signing requires X-Session-Id")
	}
	if clientVersion == "" {
		clientVersion = SignClientVersion
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	nonce := randomHex(SignNonceHex)
	pow, err := SolveProofOfWork(apiKeyID, SignAppID, sessionID, ts, SignPoWBits)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"X-Client-Ts":      ts,
		"X-Client-Version": clientVersion,
		"X-App-Id":         SignAppID,
		"X-Client-Nonce":   nonce,
		"X-Client-Pow":     pow,
		"X-Client-Sig":     SignRequest(priv, apiKeyID, ts, clientVersion, sessionID, nonce),
	}, nil
}

// StripSigningHeaders removes every X-Client-* identity header the signer owns,
// so retried requests never carry a stale signature.
func StripSigningHeaders(headers map[string][]string) map[string][]string {
	out := make(map[string][]string, len(headers))
	for k, vs := range headers {
		switch http.CanonicalHeaderKey(k) {
		case "X-Client-Ts", "X-Client-Version", "X-Client-Sig", "X-Client-Nonce", "X-Client-Pow", "X-App-Id", "X-Client-Sign-Verified":
			continue
		}
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}

// HasRefreshableSignatureReason reports whether an upstream 401 body carries a
// reason that a fresh handshake can fix.
func HasRefreshableSignatureReason(statusCode int, body []byte) bool {
	if statusCode != http.StatusUnauthorized {
		return false
	}
	var envelope struct {
		Msg    string `json:"msg"`
		Reason string `json:"reason"`
		Data   struct {
			Reason string `json:"reason"`
		} `json:"data"`
		Error struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	for _, v := range []string{envelope.Msg, envelope.Reason, envelope.Data.Reason, envelope.Error.Reason, envelope.Error.Message} {
		if signRefreshReasons[v] {
			return true
		}
	}
	return false
}

// SigningHandshakeURL resolves the handshake endpoint for a provider base URL.
func SigningHandshakeURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("zcode signing: invalid baseURL: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", errors.New("zcode signing: handshake requires HTTPS")
	}
	return parsed.Scheme + "://" + parsed.Host + SignHandshakePath, nil
}

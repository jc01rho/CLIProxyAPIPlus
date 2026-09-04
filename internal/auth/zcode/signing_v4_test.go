package zcode

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandshakeSignatureKnownVectors pins the handshake HMAC message format:
// "get_sign_key\n{apiKeyId}\n{ts}\n{nonce}" with HKDF(ikm=secret).
func TestHandshakeSignatureKnownVectors(t *testing.T) {
	id, secret, ts, nonce := "abc123", "supersecret", "1740000000000", "0011223344556677"
	sig, err := HandshakeSignature(id, secret, ts, nonce)
	if err != nil {
		t.Fatalf("HandshakeSignature: %v", err)
	}
	key, err := hkdf.Key(sha256.New, []byte(secret), []byte(SignHKDFSalt), SignInfoHandshake, 32)
	if err != nil {
		t.Fatalf("hkdf: %v", err)
	}
	mac := sha256Sum([]byte("get_sign_key\n"+id+"\n"+ts+"\n"+nonce), key)
	expected := base64.StdEncoding.EncodeToString(mac)
	if sig != expected {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", sig, expected)
	}
}

// TestParseSigningCredential rejects malformed credentials.
func TestParseSigningCredential(t *testing.T) {
	cases := []struct {
		in      string
		wantID  string
		wantSec string
		wantErr bool
	}{
		{"id.secret", "id", "secret", false},
		{"id", "", "", true},
		{"", "", "", true},
		{".secret", "", "", true},
		{"id.", "", "", true},
		{"a.b.c", "", "", true},
		{"id.sec.ret", "", "", true},
	}
	for _, tc := range cases {
		id, sec, err := ParseSigningCredential(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("ParseSigningCredential(%q): want error, got none", tc.in)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ParseSigningCredential(%q): want ok, got %v", tc.in, err)
		}
		if !tc.wantErr && (id != tc.wantID || sec != tc.wantSec) {
			t.Errorf("ParseSigningCredential(%q): got (%q,%q)", tc.in, id, sec)
		}
	}
}

// TestSolveProofOfWork verifies the salt/attempt layout and the zero-bit proof.
func TestSolveProofOfWork(t *testing.T) {
	attempt, err := SolveProofOfWork("key123", "zcode", "sess", "1740000000000", SignPoWBits)
	if err != nil {
		t.Fatalf("SolveProofOfWork: %v", err)
	}
	if len(attempt) != 32 {
		t.Fatalf("attempt length = %d, want 32", len(attempt))
	}
	saltSum := sha256.Sum256([]byte("key123\nzcode\nsess\n1740000000000"))
	salt := hexEncode(saltSum[:])[:32]
	digest := sha256.Sum256([]byte(salt + "\n" + attempt))
	if !leadingZeroBits(digest[:], SignPoWBits) {
		t.Fatalf("returned attempt does not satisfy %d-bit proof", SignPoWBits)
	}
	// Counter part must be the last 8 hex chars and parse as a small integer.
	for _, c := range attempt[12:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("counter part %q is not hex", attempt[12:])
		}
	}
}

// TestLeadingZeroBitsEdgeCases covers partial-byte boundaries.
func TestLeadingZeroBitsEdgeCases(t *testing.T) {
	if !leadingZeroBits([]byte{0x00, 0x00, 0x00}, 24) {
		t.Error("24 zero bits on 3 zero bytes should pass")
	}
	if leadingZeroBits([]byte{0x01, 0x00, 0x00}, 24) {
		t.Error("0x01 must fail 24-bit check")
	}
	if leadingZeroBits([]byte{0x00, 0x80}, 9) {
		t.Error("0x00 0x80 has only 8 leading zero bits, must fail 9")
	}
	if !leadingZeroBits([]byte{0x00, 0x7F}, 9) {
		t.Error("0x00 0x7F has 9 leading zero bits (top bit of second byte clear)")
	}
	if !leadingZeroBits([]byte{0x00, 0x40}, 9) {
		t.Error("0x00 0x40 has exactly 9 leading zero bits (0x40 = 0b01000000)")
	}
	if leadingZeroBits([]byte{0x00, 0x40}, 10) {
		t.Error("0x00 0x40 has only 9 leading zero bits, must fail 10")
	}
	if !leadingZeroBits([]byte{0x00, 0xFF}, 8) {
		t.Error("first byte zero satisfies 8 bits regardless of the second")
	}
}

// TestSignRequestMessage pins the 5-field newline message.
func TestSignRequestMessage(t *testing.T) {
	got := string(SignRequestMessage("id", "123", "3.10.2", "sess", "nonce"))
	want := "id\n123\n3.10.2\nsess\nnonce"
	if got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

// TestSignRequestRoundTrip signs a message and verifies with the public key.
func TestSignRequestRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sig := SignRequest(priv, "id", "1740000000000", "3.10.2", "sess", "ab"+"cd")
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatalf("signature is not base64: %v", err)
	}
	if !ed25519.Verify(pub, SignRequestMessage("id", "1740000000000", "3.10.2", "sess", "abcd"), raw) {
		t.Fatal("signature does not verify")
	}
}

// TestPrivateKeyFromCipherRoundTrip encrypts a synthetic PKCS8 Ed25519 key with
// the same AES-GCM layout and verifies decryption.
func TestPrivateKeyFromCipherRoundTrip(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pkcs8, err := marshalEd25519PKCS8(priv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cipherText, err := sealPrivateCipher("someid", "somelongsecret", pkcs8)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	restored, err := PrivateKeyFromCipher("someid", "somelongsecret", cipherText)
	if err != nil {
		t.Fatalf("PrivateKeyFromCipher: %v", err)
	}
	if !bytes.Equal(priv, restored) {
		t.Fatal("decrypted key differs from the original")
	}
	// Wrong AAD (apiKeyID) must fail.
	if _, err := PrivateKeyFromCipher("otherid", "somelongsecret", cipherText); err == nil {
		t.Fatal("decryption with wrong apiKeyID should fail")
	}
}

// TestHandshakeEndToEnd spins a mock get_sign_key server that mirrors the
// observed response envelope and verifies the full client flow.
func TestHandshakeEndToEnd(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pkcs8, err := marshalEd25519PKCS8(priv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != SignHandshakePath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer keyid.secretvalue" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			APIKey string `json:"apiKey"`
			Nonce  string `json:"nonce"`
			Sig    string `json:"sig"`
			Ts     string `json:"ts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		wantSig, _ := HandshakeSignature("keyid", "secretvalue", body.Ts, body.Nonce)
		if body.Sig != wantSig {
			t.Errorf("body sig mismatch: got %q want %q", body.Sig, wantSig)
		}
		sealed, err := sealPrivateCipher("keyid", "secretvalue", pkcs8)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		_ = srv
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"privateCipher": sealed}})
	}))
	defer srv.Close()

	restored, err := Handshake(t.Context(), srv.URL, "keyid.secretvalue", srv.Client())
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if !bytes.Equal(priv, restored) {
		t.Fatal("restored key differs")
	}
	if !pub.Equal(restored.Public()) {
		t.Fatal("public key mismatch")
	}
}

// TestHandshakeRejectsBusinessError verifies non-200 codes surface as errors.
func TestHandshakeRejectsBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 403, "msg": "HANDSHAKE_RATE_LIMITED"})
	}))
	defer srv.Close()
	if _, err := Handshake(t.Context(), srv.URL, "keyid.secretvalue", srv.Client()); err == nil {
		t.Fatal("business rejection should return an error")
	}
}

// TestHasRefreshableSignatureReason checks the retry-reason parser.
func TestHasRefreshableSignatureReason(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"invalid signature", 401, `{"msg":"VERIFY_SIGNATURE_INVALID"}`, true},
		{"expired api key", 401, `{"error":{"reason":"VERIFY_APIKEY_EXPIRED"}}`, true},
		{"other reason", 401, `{"msg":"VERIFY_CAPTCHA_FAILED"}`, false},
		{"wrong status", 403, `{"msg":"VERIFY_SIGNATURE_INVALID"}`, false},
		{"not json", 401, `plain text`, false},
	}
	for _, tc := range cases {
		got := HasRefreshableSignatureReason(tc.status, []byte(tc.body))
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStripSigningHeaders removes the exact identity headers.
func TestStripSigningHeaders(t *testing.T) {
	in := map[string][]string{
		"X-Client-Ts":            {"1"},
		"x-client-sig":           {"sig"},
		"X-App-Id":               {"zcode"},
		"X-Client-Sign-Verified": {"true"},
		"Authorization":          {"Bearer x"},
		"X-Session-Id":           {"sess"},
	}
	out := StripSigningHeaders(in)
	if _, ok := out["X-Client-Ts"]; ok {
		t.Error("X-Client-Ts should be stripped")
	}
	if _, ok := out["X-Client-Sig"]; ok {
		t.Error("X-Client-Sig should be stripped")
	}
	if _, ok := out["X-App-Id"]; ok {
		t.Error("X-App-Id should be stripped")
	}
	if _, ok := out["X-Client-Sign-Verified"]; ok {
		t.Error("X-Client-Sign-Verified should be stripped")
	}
	if out["X-Session-Id"] == nil || out["X-Session-Id"][0] != "sess" {
		t.Error("X-Session-Id must survive stripping")
	}
	if out["Authorization"] == nil || out["Authorization"][0] != "Bearer x" {
		t.Error("Authorization must survive stripping")
	}
}

// TestBuildSigningHeadersProducesFullSet verifies the six-header output.
func TestBuildSigningHeadersProducesFullSet(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	headers, err := BuildSigningHeaders(priv, "id", "sess", "", fixedTime())
	if err != nil {
		t.Fatalf("BuildSigningHeaders: %v", err)
	}
	if headers["X-Client-Version"] != SignClientVersion {
		t.Errorf("version = %q", headers["X-Client-Version"])
	}
	if headers["X-App-Id"] != SignAppID {
		t.Errorf("app id = %q", headers["X-App-Id"])
	}
	if headers["X-Client-Ts"] == "" || headers["X-Client-Nonce"] == "" || headers["X-Client-Pow"] == "" || headers["X-Client-Sig"] == "" {
		t.Error("missing header value in output")
	}
	if _, err := BuildSigningHeaders(priv, "id", "", "", fixedTime()); err == nil {
		t.Error("empty session id must be rejected")
	}
}

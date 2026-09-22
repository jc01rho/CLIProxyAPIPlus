package mimocode

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOAuthServerReceivesCallbackAndHonorsContext(t *testing.T) {
	server, errStart := StartOAuthServer()
	if errStart != nil {
		t.Fatalf("StartOAuthServer() error = %v", errStart)
	}
	defer func() { _ = server.Close(context.Background()) }()

	response, errGet := http.Get(server.RedirectURI() + "?u=known-payload")
	if errGet != nil {
		t.Fatalf("callback GET error = %v", errGet)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	payload, errWait := server.Wait(context.Background())
	if errWait != nil || payload != "known-payload" {
		t.Fatalf("Wait() = %q, %v", payload, errWait)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, errCancelled := server.Wait(cancelled); errCancelled == nil {
		t.Fatal("Wait() with cancelled context returned nil error")
	}
}

func TestDecryptPayloadRoundTrip(t *testing.T) {
	publicB64, privateDER, errGen := GenerateKeyPair()
	if errGen != nil {
		t.Fatalf("GenerateKeyPair: %v", errGen)
	}
	parsed, errParse := x509.ParsePKCS8PrivateKey(privateDER)
	if errParse != nil {
		t.Fatalf("parse key: %v", errParse)
	}
	recipient := parsed.(*ecdh.PrivateKey)
	ephemeral, errEph := ecdh.X25519().GenerateKey(rand.Reader)
	if errEph != nil {
		t.Fatalf("ephemeral: %v", errEph)
	}
	shared, errECDH := ephemeral.ECDH(recipient.PublicKey())
	if errECDH != nil {
		t.Fatalf("ecdh: %v", errECDH)
	}
	key := sha256.Sum256(shared)
	block, errAES := aes.NewCipher(key[:])
	if errAES != nil {
		t.Fatalf("aes: %v", errAES)
	}
	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		t.Fatalf("gcm: %v", errGCM)
	}
	nonce := make([]byte, 12)
	if _, errRead := rand.Read(nonce); errRead != nil {
		t.Fatalf("nonce: %v", errRead)
	}
	plain, errJSON := json.Marshal(Credentials{SK: "sk-test", UID: "uid-1", URL: "https://api.example.test/v1"})
	if errJSON != nil {
		t.Fatalf("marshal: %v", errJSON)
	}
	sealed := gcm.Seal(nil, nonce, plain, nil)
	blob := append(append(ephemeral.PublicKey().Bytes(), nonce...), sealed...)
	encoded := base64.RawURLEncoding.EncodeToString(blob)

	got, errDec := DecryptPayload(privateDER, encoded)
	if errDec != nil {
		t.Fatalf("DecryptPayload: %v", errDec)
	}
	if got.SK != "sk-test" || got.UID != "uid-1" || got.URL != "https://api.example.test/v1" {
		t.Fatalf("credentials = %+v", got)
	}
	authURL := BuildAuthorizeURL(publicB64, "http://127.0.0.1:1455/callback", "desk")
	for _, part := range []string{"pk=" + publicB64, "kn=mimocode", "key_name=desk"} {
		if !strings.Contains(authURL, part) {
			t.Fatalf("authorize url %s missing %s", authURL, part)
		}
	}
}

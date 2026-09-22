package management

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	mimocodeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/mimocode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestMimocodeReloginIdentityReusesPersistedKeyName(t *testing.T) {
	authDir := t.TempDir()
	fileName := "mimocode-user.json"
	if errWrite := os.WriteFile(filepath.Join(authDir, fileName), []byte(`{"type":"api","provider":"mimocode","key_name":"stable-key"}`), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	h := NewHandler(&config.Config{AuthDir: authDir}, "", nil)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("GET", "/v0/management/mimocode-auth-url?name="+fileName, nil)
	keyName, gotFileName := h.mimocodeReloginIdentity(ctx)
	if keyName != "stable-key" || gotFileName != fileName {
		t.Fatalf("mimocodeReloginIdentity() = %q/%q", keyName, gotFileName)
	}
}

func TestCompleteMimocodeAuthorizationPersistsAPIAuth(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandler(&config.Config{AuthDir: authDir}, "", nil)
	state := "mimocode-test-state"
	RegisterOAuthSession(state, "mimocode")
	publicKey, privateKeyDER, errGenerate := mimocodeauth.GenerateKeyPair()
	if errGenerate != nil {
		t.Fatalf("GenerateKeyPair() error = %v", errGenerate)
	}
	payload := encryptMimocodeTestPayload(t, publicKey, mimocodeauth.Credentials{
		SK: "sk-test", UID: "user-123", URL: "https://region.example/v1/",
	})
	if errComplete := h.completeMimocodeAuthorization(context.Background(), state, payload, privateKeyDER, "stable-key-name", ""); errComplete != nil {
		t.Fatalf("completeMimocodeAuthorization() error = %v", errComplete)
	}
	data, errRead := os.ReadFile(filepath.Join(authDir, "mimocode-user-123.json"))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var stored map[string]any
	if errJSON := json.Unmarshal(data, &stored); errJSON != nil {
		t.Fatalf("Unmarshal() error = %v", errJSON)
	}
	for key, want := range map[string]string{
		"type": "api", "provider": "mimocode", "api_key": "sk-test", "uid": "user-123",
		"base_url": "https://region.example/v1", "key_name": "stable-key-name",
	} {
		if got, _ := stored[key].(string); got != want {
			t.Fatalf("stored[%q] = %q, want %q; stored=%+v", key, got, want, stored)
		}
	}
}

func encryptMimocodeTestPayload(t *testing.T, recipientPublicB64 string, credentials mimocodeauth.Credentials) string {
	t.Helper()
	recipientBytes, errDecode := base64.RawURLEncoding.DecodeString(recipientPublicB64)
	if errDecode != nil {
		t.Fatalf("decode recipient key: %v", errDecode)
	}
	recipientPublic, errPublic := ecdh.X25519().NewPublicKey(recipientBytes)
	if errPublic != nil {
		t.Fatalf("NewPublicKey() error = %v", errPublic)
	}
	ephemeralPrivate, errEphemeral := ecdh.X25519().GenerateKey(rand.Reader)
	if errEphemeral != nil {
		t.Fatalf("GenerateKey() error = %v", errEphemeral)
	}
	sharedSecret, errECDH := ephemeralPrivate.ECDH(recipientPublic)
	if errECDH != nil {
		t.Fatalf("ECDH() error = %v", errECDH)
	}
	key := sha256.Sum256(sharedSecret)
	block, errAES := aes.NewCipher(key[:])
	if errAES != nil {
		t.Fatalf("NewCipher() error = %v", errAES)
	}
	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		t.Fatalf("NewGCM() error = %v", errGCM)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, errRead := rand.Read(nonce); errRead != nil {
		t.Fatalf("rand.Read() error = %v", errRead)
	}
	plaintext, _ := json.Marshal(credentials)
	ciphertextAndTag := gcm.Seal(nil, nonce, plaintext, nil)
	payload := append(append(append([]byte{}, ephemeralPrivate.PublicKey().Bytes()...), nonce...), ciphertextAndTag...)
	return base64.RawURLEncoding.EncodeToString(payload)
}

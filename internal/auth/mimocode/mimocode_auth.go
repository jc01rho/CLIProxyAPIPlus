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
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const AuthorizeEndpoint = "https://platform.xiaomimimo.com/authorize"

// Credentials is the decrypted API credential returned by MiMo authorization.
type Credentials struct {
	SK  string `json:"sk"`
	UID string `json:"uid"`
	URL string `json:"url"`
}

// GenerateKeyPair creates an X25519 key pair. The public key is base64url raw
// bytes and the private key is PKCS#8 DER.
func GenerateKeyPair() (publicKeyB64 string, privateKeyDER []byte, err error) {
	privateKey, errGenerate := ecdh.X25519().GenerateKey(rand.Reader)
	if errGenerate != nil {
		return "", nil, fmt.Errorf("generate X25519 key: %w", errGenerate)
	}
	privateKeyDER, err = x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", nil, fmt.Errorf("marshal X25519 private key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()), privateKeyDER, nil
}

// BuildAuthorizeURL constructs the MiMo platform authorization URL.
func BuildAuthorizeURL(publicKeyB64, redirectURI, keyName string) string {
	values := url.Values{}
	values.Set("pk", strings.TrimSpace(publicKeyB64))
	values.Set("redirect_uri", strings.TrimSpace(redirectURI))
	values.Set("kn", "mimocode")
	values.Set("key_name", strings.TrimSpace(keyName))
	return AuthorizeEndpoint + "?" + values.Encode()
}

// DecryptPayload decrypts u = ephemeralPub32 || nonce12 || ciphertext || tag16.
func DecryptPayload(privateKeyDER []byte, payloadB64 string) (*Credentials, error) {
	parsed, errParse := x509.ParsePKCS8PrivateKey(privateKeyDER)
	if errParse != nil {
		return nil, fmt.Errorf("parse X25519 private key: %w", errParse)
	}
	privateKey, ok := parsed.(*ecdh.PrivateKey)
	if !ok || privateKey.Curve() != ecdh.X25519() {
		return nil, fmt.Errorf("private key is not X25519")
	}
	payload, errDecode := decodeBase64URL(payloadB64)
	if errDecode != nil {
		return nil, fmt.Errorf("decode authorization payload: %w", errDecode)
	}
	if len(payload) < 32+12+16 {
		return nil, fmt.Errorf("authorization payload is too short")
	}
	ephemeralPublic, errPublic := ecdh.X25519().NewPublicKey(payload[:32])
	if errPublic != nil {
		return nil, fmt.Errorf("parse ephemeral X25519 key: %w", errPublic)
	}
	sharedSecret, errECDH := privateKey.ECDH(ephemeralPublic)
	if errECDH != nil {
		return nil, fmt.Errorf("derive X25519 shared secret: %w", errECDH)
	}
	key := sha256.Sum256(sharedSecret)
	block, errAES := aes.NewCipher(key[:])
	if errAES != nil {
		return nil, fmt.Errorf("create AES cipher: %w", errAES)
	}
	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", errGCM)
	}
	nonce := payload[32:44]
	ciphertextAndTag := payload[44:]
	plaintext, errOpen := gcm.Open(nil, nonce, ciphertextAndTag, nil)
	if errOpen != nil {
		return nil, fmt.Errorf("decrypt authorization payload: %w", errOpen)
	}
	var credentials Credentials
	if errJSON := json.Unmarshal(plaintext, &credentials); errJSON != nil {
		return nil, fmt.Errorf("decode authorization credentials: %w", errJSON)
	}
	credentials.SK = strings.TrimSpace(credentials.SK)
	credentials.UID = strings.TrimSpace(credentials.UID)
	credentials.URL = strings.TrimSpace(credentials.URL)
	if credentials.SK == "" {
		return nil, fmt.Errorf("authorization payload contains an empty api key")
	}
	return &credentials, nil
}

func decodeBase64URL(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("payload is empty")
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

// OAuthServer handles one localhost authorization callback.
type OAuthServer struct {
	listener net.Listener
	server   *http.Server
	result    chan string
	once      sync.Once
	closed    chan struct{}
	closeOnce sync.Once
}

// StartOAuthServer binds a localhost callback server on an ephemeral port.
func StartOAuthServer() (*OAuthServer, error) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		return nil, fmt.Errorf("start mimocode callback listener: %w", errListen)
	}
	server := &OAuthServer{listener: listener, result: make(chan string, 1), closed: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", server.handleCallback)
	server.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		_ = server.server.Serve(listener)
		server.closeOnce.Do(func() { close(server.closed) })
	}()
	return server, nil
}

func (s *OAuthServer) RedirectURI() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return "http://" + s.listener.Addr().String() + "/callback"
}

func (s *OAuthServer) Wait(ctx context.Context) (string, error) {
	if s == nil {
		return "", fmt.Errorf("mimocode callback server is nil")
	}
	select {
	case payload := <-s.result:
		return payload, nil
	case <-s.closed:
		return "", fmt.Errorf("mimocode callback server closed")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *OAuthServer) Close(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	err := s.server.Shutdown(ctx)
	s.closeOnce.Do(func() { close(s.closed) })
	return err
}

func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload := strings.TrimSpace(r.URL.Query().Get("u"))
	if payload == "" {
		http.Error(w, "missing u", http.StatusBadRequest)
		return
	}
	s.once.Do(func() { s.result <- payload })
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("MiMo authentication complete. You can close this window."))
}

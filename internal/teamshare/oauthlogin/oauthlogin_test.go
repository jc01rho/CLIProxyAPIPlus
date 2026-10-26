package oauthlogin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRedactURLRemovesQueryValues(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://example.com/cb?code=SECRET&state=XYZ", "https://example.com/cb?code=[redacted]&state=[redacted]"},
		{"https://example.com/cb", "https://example.com/cb"},
		{"https://example.com/cb#access_token=SECRET", "https://example.com/cb#[redacted]"},
		{"https://user:pass@example.com/p?a=b", "https://[redacted]@example.com/p?a=[redacted]"},
		{"", ""},
	}
	for _, c := range cases {
		if got := RedactURL(c.in); got != c.want {
			t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, secret := range []string{"SECRET", "XYZ", "pass"} {
		for _, c := range cases {
			if got := RedactURL(c.in); strings.Contains(got, secret) {
				t.Errorf("RedactURL(%q) leaked %q: %q", c.in, secret, got)
			}
		}
	}
	msg := redactText("navigated to https://example.com/cb?code=SECRET now")
	if strings.Contains(msg, "SECRET") {
		t.Errorf("redactText leaked secret: %q", msg)
	}
	if !strings.Contains(msg, "https://example.com/cb?code=[redacted]") {
		t.Errorf("redactText mangled message: %q", msg)
	}
}

func TestToStdBase64AcceptsBothAlphabets(t *testing.T) {
	raw := []byte{0xfb, 0xff, 0x00, 0x3e, 0x3f}
	std := base64.StdEncoding.EncodeToString(raw)
	for _, in := range []string{
		std,
		base64.RawStdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
	} {
		got, err := toStdBase64(in)
		if err != nil {
			t.Fatalf("toStdBase64(%q): %v", in, err)
		}
		if got != std {
			t.Errorf("toStdBase64(%q) = %q, want %q", in, got, std)
		}
	}
	if _, err := toStdBase64("  "); err == nil {
		t.Error("expected error for empty value")
	}
}

// ---------------------------------------------------------------------------
// Real-Chromium tests
// ---------------------------------------------------------------------------

func requireChromium(t *testing.T) string {
	t.Helper()
	path, err := FindChromium()
	if err != nil {
		t.Skipf("skipping: no chromium available: %v", err)
	}
	t.Logf("using chromium: %s", path)
	return path
}

type assertionPayload struct {
	ID                string `json:"id"`
	ClientDataJSON    string `json:"clientDataJSON"`
	AuthenticatorData string `json:"authenticatorData"`
	Signature         string `json:"signature"`
	UserHandle        string `json:"userHandle"`
}

type webauthnServer struct {
	srv       *httptest.Server
	baseURL   string
	challenge []byte

	mu        sync.Mutex
	assertion *assertionPayload
	pageError string
}

// newWebAuthnServer serves a page that performs navigator.credentials.get with
// the supplied credential and then redirects to the OAuth callback URL.
func newWebAuthnServer(t *testing.T, credentialID []byte, callbackURL string) *webauthnServer {
	t.Helper()
	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		t.Fatalf("challenge: %v", err)
	}
	ws := &webauthnServer{challenge: challenge}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	ws.baseURL = fmt.Sprintf("http://localhost:%d", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		fmt.Fprint(w, loginPage(challenge, credentialID))
	})
	mux.HandleFunc("/assert", func(w http.ResponseWriter, r *http.Request) {
		var payload assertionPayload
		if errDecode := json.NewDecoder(r.Body).Decode(&payload); errDecode != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		ws.mu.Lock()
		ws.assertion = &payload
		ws.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/error", func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAllString(r)
		ws.mu.Lock()
		ws.pageError = body
		ws.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/done", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, callbackURL, http.StatusFound)
	})

	srv := &httptest.Server{Listener: listener, Config: &http.Server{Handler: mux}}
	srv.Start()
	ws.srv = srv
	t.Cleanup(srv.Close)
	return ws
}

func readAllString(r *http.Request) (string, error) {
	buf := make([]byte, 4096)
	n, err := r.Body.Read(buf)
	if n > 0 {
		return string(buf[:n]), nil
	}
	return "", err
}

func loginPage(challenge, credentialID []byte) string {
	return fmt.Sprintf(`<!doctype html>
<html><body>
<button id="login">login</button>
<script>
function b64urlToBuf(s) {
  s = s.replace(/-/g, '+').replace(/_/g, '/');
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}
function bufToB64url(b) {
  const bytes = new Uint8Array(b);
  let s = '';
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
document.getElementById('login').addEventListener('click', async () => {
  try {
    const assertion = await navigator.credentials.get({publicKey: {
      challenge: b64urlToBuf(%q),
      rpId: 'localhost',
      allowCredentials: [{id: b64urlToBuf(%q), type: 'public-key'}],
      userVerification: 'required',
      timeout: 30000
    }});
    const body = {
      id: assertion.id,
      clientDataJSON: bufToB64url(assertion.response.clientDataJSON),
      authenticatorData: bufToB64url(assertion.response.authenticatorData),
      signature: bufToB64url(assertion.response.signature),
      userHandle: assertion.response.userHandle ? bufToB64url(assertion.response.userHandle) : ''
    };
    const resp = await fetch('/assert', {method: 'POST', headers: {'content-type': 'application/json'}, body: JSON.stringify(body)});
    if (!resp.ok) { await fetch('/error', {method: 'POST', body: 'assert rejected'}); return; }
    window.location = '/done';
  } catch (e) {
    await fetch('/error', {method: 'POST', body: String(e)});
  }
});
</script>
</body></html>`,
		base64.RawURLEncoding.EncodeToString(challenge),
		base64.RawURLEncoding.EncodeToString(credentialID))
}

// unusedPort returns a port nothing listens on, so the OAuth callback request
// is refused exactly like a real local redirect target that is not running.
func unusedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

type logCapture struct {
	mu     sync.Mutex
	lines  []string
	t      *testing.T
	pidCh  chan int
	once   sync.Once
	navCh  chan struct{}
	navOne sync.Once
}

func newLogCapture(t *testing.T) *logCapture {
	return &logCapture{t: t, pidCh: make(chan int, 1), navCh: make(chan struct{})}
}

var pidRe = regexp.MustCompile(`pid=(\d+), profile=(\S+)\)`)

func (l *logCapture) Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
	l.t.Logf("oauthlogin: %s", line)
	if m := pidRe.FindStringSubmatch(line); m != nil {
		pid, _ := strconv.Atoi(m[1])
		l.once.Do(func() { l.pidCh <- pid })
	}
	if strings.HasPrefix(line, "navigated to ") {
		l.navOne.Do(func() { close(l.navCh) })
	}
}

func (l *logCapture) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *logCapture) browserPIDAndProfile(t *testing.T) (int, string) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if m := pidRe.FindStringSubmatch(line); m != nil {
			pid, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("bad pid in %q", line)
			}
			return pid, m[2]
		}
	}
	t.Fatalf("no browser start line in logs:\n%s", strings.Join(l.lines, "\n"))
	return 0, ""
}

func TestRunCompletesWebAuthnLogin(t *testing.T) {
	chromium := requireChromium(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	credentialID := make([]byte, 32)
	if _, err = rand.Read(credentialID); err != nil {
		t.Fatalf("credential id: %v", err)
	}
	userHandle := []byte("service-account-1")

	callbackPort := unusedPort(t)
	callbackPrefix := fmt.Sprintf("http://127.0.0.1:%d/callback", callbackPort)
	callbackURL := callbackPrefix + "?code=TESTCODE&state=S"

	server := newWebAuthnServer(t, credentialID, callbackURL)

	const initialSignCount = 7
	logs := newLogCapture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := Run(ctx, server.baseURL+"/login", Options{
		ChromiumPath: chromium,
		Headless:     true,
		Credential: &VirtualCredential{
			CredentialID:    base64.RawURLEncoding.EncodeToString(credentialID),
			PrivateKeyPKCS8: base64.StdEncoding.EncodeToString(pkcs8),
			RPID:            "localhost",
			UserHandle:      base64.RawURLEncoding.EncodeToString(userHandle),
			SignCount:       initialSignCount,
		},
		Steps: []Step{
			{Action: ActionWaitSelector, Selector: "#login", Timeout: 20 * time.Second},
			{Action: ActionClick, Selector: "#login", Timeout: 20 * time.Second},
		},
		CallbackURLPrefix: callbackPrefix,
		Timeout:           80 * time.Second,
		Logf:              logs.Logf,
	})
	server.mu.Lock()
	pageErr := server.pageError
	assertion := server.assertion
	server.mu.Unlock()
	if err != nil {
		t.Fatalf("Run failed: %v (page error: %q)", err, pageErr)
	}
	if pageErr != "" {
		t.Fatalf("page reported WebAuthn error: %s", pageErr)
	}
	if !strings.Contains(result.FinalURL, "code=TESTCODE") {
		t.Fatalf("FinalURL = %q, want code=TESTCODE", result.FinalURL)
	}
	if !strings.HasPrefix(result.FinalURL, callbackPrefix) {
		t.Fatalf("FinalURL = %q, want prefix %q", result.FinalURL, callbackPrefix)
	}

	// The assertion must verify against the test public key: that proves the
	// virtual authenticator really signed with the installed passkey.
	if assertion == nil {
		t.Fatal("server never received a WebAuthn assertion")
	}
	clientDataJSON := mustB64URL(t, assertion.ClientDataJSON)
	authData := mustB64URL(t, assertion.AuthenticatorData)
	signature := mustB64URL(t, assertion.Signature)

	var clientData struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if errUnmarshal := json.Unmarshal(clientDataJSON, &clientData); errUnmarshal != nil {
		t.Fatalf("clientDataJSON: %v", errUnmarshal)
	}
	if clientData.Type != "webauthn.get" {
		t.Errorf("clientData.type = %q", clientData.Type)
	}
	if clientData.Challenge != base64.RawURLEncoding.EncodeToString(server.challenge) {
		t.Errorf("challenge mismatch: %q", clientData.Challenge)
	}
	if clientData.Origin != server.baseURL {
		t.Errorf("origin = %q, want %q", clientData.Origin, server.baseURL)
	}

	digest := sha256.Sum256(clientDataJSON)
	signed := append(append([]byte{}, authData...), digest[:]...)
	signedDigest := sha256.Sum256(signed)
	if !ecdsa.VerifyASN1(&key.PublicKey, signedDigest[:], signature) {
		t.Fatal("assertion signature does not verify against the test public key")
	}
	if len(authData) < 37 {
		t.Fatalf("authenticator data too short: %d", len(authData))
	}
	authDataSignCount := binary.BigEndian.Uint32(authData[33:37])
	if authDataSignCount <= initialSignCount {
		t.Errorf("authData signCount = %d, want > %d", authDataSignCount, initialSignCount)
	}
	if assertion.UserHandle != base64.RawURLEncoding.EncodeToString(userHandle) {
		t.Errorf("userHandle = %q, want %q", assertion.UserHandle, base64.RawURLEncoding.EncodeToString(userHandle))
	}

	if result.SignCount <= initialSignCount {
		t.Errorf("Result.SignCount = %d, want > %d", result.SignCount, initialSignCount)
	}

	// No secret may appear in the log output.
	logged := logs.all()
	for _, secret := range []string{"TESTCODE", base64.StdEncoding.EncodeToString(pkcs8), base64.RawURLEncoding.EncodeToString(credentialID)} {
		if strings.Contains(logged, secret) {
			t.Errorf("logs leaked a secret:\n%s", logged)
		}
	}

	pid, profile := logs.browserPIDAndProfile(t)
	assertBrowserGone(t, pid, profile)
}

func TestRunContextCancellationKillsBrowser(t *testing.T) {
	chromium := requireChromium(t)

	callbackPort := unusedPort(t)
	callbackPrefix := fmt.Sprintf("http://127.0.0.1:%d/callback", callbackPort)

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><body><div id="idle">idle</div></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	logs := newLogCapture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, srv.URL+"/login", Options{
			ChromiumPath: chromium,
			Headless:     true,
			Steps: []Step{
				{Action: ActionWaitURL, URLContains: "never-happens", Timeout: 60 * time.Second},
			},
			CallbackURLPrefix: callbackPrefix,
			Timeout:           60 * time.Second,
			Logf:              logs.Logf,
		})
		done <- err
	}()

	var pid int
	select {
	case pid = <-logs.pidCh:
	case err := <-done:
		t.Fatalf("Run returned before the browser started: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("browser never started")
	}
	// Cancel only once the page is actually loading, so the kill path is
	// exercised with a live DevTools session and a running renderer.
	select {
	case <-logs.navCh:
	case err := <-done:
		t.Fatalf("Run returned before navigation: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("navigation never started")
	}

	cancel()
	start := time.Now()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
		if elapsed := time.Since(start); elapsed > 20*time.Second {
			t.Errorf("Run took %s to return after cancellation", elapsed)
		}
		t.Logf("Run returned %v after %s", err, time.Since(start))
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	_, profile := logs.browserPIDAndProfile(t)
	assertBrowserGone(t, pid, profile)
}

func assertBrowserGone(t *testing.T, pid int, profile string) {
	t.Helper()
	if err := syscall.Kill(pid, 0); err == nil {
		// The process may still be a zombie reaped by cmd.Wait; give the OS a
		// moment only if signalling still succeeds.
		t.Errorf("chromium pid %d is still alive", pid)
	} else if err != syscall.ESRCH && err != syscall.EPERM {
		t.Errorf("unexpected kill(%d, 0) error: %v", pid, err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Errorf("temporary profile %s was not removed (stat err: %v)", profile, err)
	}
	if !strings.Contains(profile, "oauthlogin-profile-") {
		t.Errorf("unexpected profile path %q", profile)
	}
}

func mustB64URL(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		t.Fatalf("decode base64url %q: %v", s, err)
	}
	return raw
}

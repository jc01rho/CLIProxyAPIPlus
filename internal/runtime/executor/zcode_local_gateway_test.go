package executor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// capturedZcodeRequest records what a local stand-in for the ultra gateway saw.
type capturedZcodeRequest struct {
	method string
	path   string
	query  string
	header http.Header
	body   []byte
}

// newZcodeCaptureServer serves a canned Claude SSE stream and records the
// request the executor sent. Both Execute and ExecuteStream read the upstream
// as SSE; Execute only aggregates it.
func newZcodeCaptureServer(t *testing.T) (*httptest.Server, *capturedZcodeRequest) {
	t.Helper()
	seen := &capturedZcodeRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method = r.Method
		seen.path = r.URL.Path
		seen.query = r.URL.RawQuery
		seen.header = r.Header.Clone()
		seen.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_zcode\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"glm-5.3\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(server.Close)
	return server, seen
}

func zcodeLocalGatewayPayload() []byte {
	return []byte(`{"model":"glm-5.3","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
}

// assertZcodeDesktopIdentity verifies the request carries the identity a stock
// ZCode 3.11.2 desktop sends: the source headers, the per-account session id,
// and the provisioned key in the credential slot the gateway reads.
func assertZcodeDesktopIdentity(t *testing.T, seen *capturedZcodeRequest, deviceID string) {
	t.Helper()
	if seen.method != http.MethodPost {
		t.Errorf("method = %q, want POST", seen.method)
	}
	if seen.path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", seen.path)
	}
	if !strings.Contains(seen.query, "beta=true") {
		t.Errorf("query = %q, want beta=true for the Claude wire contract", seen.query)
	}
	wantUA := "ZCode/" + zcodeAppVersion() + " ai-sdk/provider-utils/4.0.27 runtime/node.js/"
	if ua := seen.header.Get("User-Agent"); !strings.HasPrefix(ua, wantUA) {
		t.Errorf("User-Agent = %q, want prefix %q", ua, wantUA)
	}
	for k, want := range map[string]string{
		"Http-Referer":        "https://zcode.z.ai",
		"X-Title":             "Z Code@electron",
		"X-Zcode-Agent":       "glm",
		"X-Zcode-App-Version": "3.11.2",
		"X-Release-Channel":   "production",
	} {
		if got := seen.header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if seen.header.Get("X-Os-Version") == "" {
		t.Error("X-Os-Version missing")
	}
	if seen.header.Get("X-Client-Language") == "" || seen.header.Get("X-Client-Timezone") == "" {
		t.Error("X-Client-Language/X-Client-Timezone missing")
	}
	if got := seen.header.Get("X-Device-Mid"); got != "" {
		t.Errorf("X-Device-Mid = %q, want unset on the agent path", got)
	}
	sessionID := seen.header.Get("X-Session-Id")
	if sessionID == "" {
		t.Fatal("X-Session-Id missing")
	}
	if got, bearer := seen.header.Get("X-Api-Key"), strings.TrimPrefix(seen.header.Get("Authorization"), "Bearer "); got != "key-1.secret" && bearer != "key-1.secret" {
		t.Errorf("provisioned key missing: X-Api-Key = %q, Authorization = %q", got, seen.header.Get("Authorization"))
	}

	var payload struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(seen.body, &payload); err != nil {
		t.Fatalf("decode request body: %v (%s)", err, seen.body)
	}
	var identity struct {
		DeviceID  string `json:"device_id"`
		AccountID string `json:"account_uuid"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(payload.Metadata.UserID), &identity); err != nil {
		t.Fatalf("metadata.user_id = %q is not the desktop JSON identity: %v", payload.Metadata.UserID, err)
	}
	if identity.DeviceID != deviceID {
		t.Errorf("metadata.user_id.device_id = %q, want %q", identity.DeviceID, deviceID)
	}
	if identity.AccountID != "" {
		t.Errorf("metadata.user_id.account_uuid = %q, want empty", identity.AccountID)
	}
	if identity.SessionID != sessionID {
		t.Errorf("metadata.user_id.session_id = %q, header X-Session-Id = %q; they must agree", identity.SessionID, sessionID)
	}
}

// TestZcodeExecuteAgainstLocalGateway drives the real Execute path against a
// local stand-in for the ultra gateway and checks the desktop identity the
// extension reference promises.
func TestZcodeExecuteAgainstLocalGateway(t *testing.T) {
	const deviceID = "device-mid-local-gateway"
	t.Setenv("ZCODE_DEVICE_ID", deviceID)
	server, seen := newZcodeCaptureServer(t)
	t.Setenv("ZCODE_ANTHROPIC_BASE_URL", server.URL)

	exec := NewZcodeExecutor(nil)
	auth := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{
		"api_key": "key-1.secret",
		"email":   "user@example.com",
	}}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.3",
		Payload: zcodeLocalGatewayPayload(),
	}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(string(resp.Payload), "hi") {
		t.Errorf("aggregated payload = %s, want the streamed text", resp.Payload)
	}
	assertZcodeDesktopIdentity(t, seen, deviceID)
}

// TestZcodeExecuteStreamAgainstLocalGateway covers the streaming path, whose
// header/identity wiring is built by a separate code path.
func TestZcodeExecuteStreamAgainstLocalGateway(t *testing.T) {
	const deviceID = "device-mid-stream"
	t.Setenv("ZCODE_DEVICE_ID", deviceID)
	server, seen := newZcodeCaptureServer(t)
	t.Setenv("ZCODE_ANTHROPIC_BASE_URL", server.URL)

	exec := NewZcodeExecutor(nil)
	auth := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{
		"api_key": "key-1.secret",
		"email":   "user@example.com",
	}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.3",
		Payload: zcodeLocalGatewayPayload(),
	}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if result == nil {
		t.Fatal("ExecuteStream() returned no stream")
	}
	chunks := 0
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		chunks++
	}
	if chunks == 0 {
		t.Error("stream produced no chunks")
	}
	assertZcodeDesktopIdentity(t, seen, deviceID)
}

// TestZcodeSignedRequestVerifiesAgainstPublicKey exercises the signed path with
// a real key: the produced X-Client-Sig must verify over the documented message
// and the X-Client-Pow must solve the documented salt with 8 leading zero bits.
func TestZcodeSignedRequestVerifiesAgainstPublicKey(t *testing.T) {
	zcodeResetSigningStateForTests()
	defer zcodeResetSigningStateForTests()

	pub, priv, errKey := ed25519.GenerateKey(rand.Reader)
	if errKey != nil {
		t.Fatalf("generate key: %v", errKey)
	}
	const credential = "key-signed.secret"
	zcodeSigningMu.Lock()
	zcodeGateStates[credential] = zcodeSignGate{enabled: true, checkedAt: time.Now()}
	zcodePrivateKeys[credential] = priv
	zcodeSigningMu.Unlock()

	exec := NewZcodeExecutor(nil)
	auth := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{
		"api_key": credential,
		"email":   "user@example.com",
	}}
	prepared, _, _ := exec.prepareZcodeRequest(context.Background(), auth, cliproxyexecutor.Request{Model: "glm-5.3"}, cliproxyexecutor.Options{})
	if prepared == nil {
		t.Fatal("prepareZcodeRequest() returned no auth")
	}

	apiKeyID, _, errParse := zcode.ParseSigningCredential(credential)
	if errParse != nil {
		t.Fatalf("parse credential: %v", errParse)
	}
	attr := func(key string) string { return prepared.Attributes["header:"+key] }
	sig, errDecode := base64.StdEncoding.DecodeString(attr("X-Client-Sig"))
	if errDecode != nil {
		t.Fatalf("X-Client-Sig = %q is not base64: %v", attr("X-Client-Sig"), errDecode)
	}
	sessionID := attr("X-Session-Id")
	if sessionID == "" {
		t.Fatal("X-Session-Id missing from the signed request")
	}
	// The signed message is {apiKeyId}\n{ts}\n{version}\n{sessionId}\n{nonce}.
	message := strings.Join([]string{
		apiKeyID,
		attr("X-Client-Ts"),
		attr("X-Client-Version"),
		sessionID,
		attr("X-Client-Nonce"),
	}, "\n")
	if !ed25519.Verify(pub, []byte(message), sig) {
		t.Errorf("signature does not verify over %q", message)
	}
	if got := attr("X-App-Id"); got != "zcode" {
		t.Errorf("X-App-Id = %q, want zcode", got)
	}
	if got := attr("X-Client-Version"); got != "3.11.2" {
		t.Errorf("X-Client-Version = %q, want 3.11.2", got)
	}
	if n := len(attr("X-Client-Nonce")); n != 32 {
		t.Errorf("X-Client-Nonce length = %d, want 32 hex chars", n)
	}

	pow := attr("X-Client-Pow")
	if len(pow) != 32 {
		t.Fatalf("X-Client-Pow = %q, want 32 hex chars", pow)
	}
	saltSum := sha256.Sum256([]byte(strings.Join([]string{apiKeyID, "zcode", sessionID, attr("X-Client-Ts")}, "\n")))
	salt := hex.EncodeToString(saltSum[:])[:32]
	digest := sha256.Sum256([]byte(salt + "\n" + pow))
	leading := 0
	for _, b := range digest {
		if b == 0 {
			leading += 8
			continue
		}
		for mask := byte(0x80); mask != 0 && b&mask == 0; mask >>= 1 {
			leading++
		}
		break
	}
	if leading < 8 {
		t.Errorf("X-Client-Pow %q has %d leading zero bits, want >= 8", pow, leading)
	}
}

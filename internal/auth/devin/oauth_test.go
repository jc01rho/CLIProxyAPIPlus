package devin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeneratePKCE(t *testing.T) {
	pkce, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE failed: %v", err)
	}
	if len(pkce.Verifier) < 43 || len(pkce.Verifier) > 128 {
		t.Errorf("expected verifier length between 43 and 128, got %d", len(pkce.Verifier))
	}
	if len(pkce.Challenge) == 0 {
		t.Errorf("challenge should not be empty")
	}
	if strings.ContainsAny(pkce.Verifier, "+/=") {
		t.Errorf("verifier should be URL-safe unpadded base64")
	}
	if strings.ContainsAny(pkce.Challenge, "+/=") {
		t.Errorf("challenge should be URL-safe unpadded base64")
	}
}

func TestGenerateAuthURL(t *testing.T) {
	pkce := &PKCECodes{
		Verifier:  "test-verifier-12345",
		Challenge: "test-challenge-67890",
	}
	state := "state-abc-123"

	authURL, err := GenerateAuthURL(state, pkce)
	if err != nil {
		t.Fatalf("GenerateAuthURL failed: %v", err)
	}

	if !strings.HasPrefix(authURL, DefaultDevinAuthorizeURL) {
		t.Errorf("expected prefix %s, got %s", DefaultDevinAuthorizeURL, authURL)
	}
	if !strings.Contains(authURL, "state=state-abc-123") {
		t.Errorf("authURL missing state")
	}
	if !strings.Contains(authURL, "code_challenge=test-challenge-67890") {
		t.Errorf("authURL missing code_challenge")
	}
	if !strings.Contains(authURL, "code_challenge_method=S256") {
		t.Errorf("authURL missing code_challenge_method")
	}
	if !strings.Contains(authURL, "cli_pkce_marker=1") {
		t.Errorf("authURL missing cli_pkce_marker")
	}
}

func TestEncodeExchangeRequest(t *testing.T) {
	code := "wspkce$my-test-code-123"
	verifier := "my-verifier-456"

	encoded := EncodeExchangeRequest(code, verifier)

	// Verify tag 1 (0x0a = (1<<3)|2)
	if len(encoded) == 0 || encoded[0] != 0x0a {
		t.Fatalf("expected first byte 0x0a, got 0x%02x", encoded[0])
	}
	// Verify "wspkce$" was stripped
	expectedCode := "my-test-code-123"
	if !strings.Contains(string(encoded), expectedCode) {
		t.Errorf("encoded payload does not contain stripped code")
	}
	if strings.Contains(string(encoded), "wspkce$") {
		t.Errorf("encoded payload should not contain 'wspkce$'")
	}
	if !strings.Contains(string(encoded), verifier) {
		t.Errorf("encoded payload does not contain verifier")
	}
}

func TestDecodeExchangeResponse(t *testing.T) {
	// Build a mock protobuf response with fields 1 to 5
	var buf []byte
	appendString := func(fieldNum int, val string) {
		tag := byte((fieldNum << 3) | 2)
		buf = append(buf, tag, byte(len(val)))
		buf = append(buf, []byte(val)...)
	}

	appendString(1, "mock-api-key-123")
	appendString(2, "https://server.codeium.com")
	appendString(3, "https://app.devin.ai")
	appendString(4, "https://api.devin.ai")
	appendString(5, "mock-session-token-456")

	resp, err := DecodeExchangeResponse(buf)
	if err != nil {
		t.Fatalf("DecodeExchangeResponse failed: %v", err)
	}

	if resp.APIKey != "mock-api-key-123" {
		t.Errorf("expected APIKey mock-api-key-123, got %s", resp.APIKey)
	}
	if resp.APIServerURL != "https://server.codeium.com" {
		t.Errorf("expected APIServerURL https://server.codeium.com, got %s", resp.APIServerURL)
	}
	if resp.DevinWebappHost != "https://app.devin.ai" {
		t.Errorf("expected DevinWebappHost https://app.devin.ai, got %s", resp.DevinWebappHost)
	}
	if resp.DevinAPIURL != "https://api.devin.ai" {
		t.Errorf("expected DevinAPIURL https://api.devin.ai, got %s", resp.DevinAPIURL)
	}
	if resp.SessionToken != "mock-session-token-456" {
		t.Errorf("expected SessionToken mock-session-token-456, got %s", resp.SessionToken)
	}
}

func TestExchangeCodeMockServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ExchangePKCEPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Content-Type") != "application/proto" {
			http.Error(w, "invalid content type", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Connect-Protocol-Version") != "1" {
			http.Error(w, "invalid connect version", http.StatusBadRequest)
			return
		}

		// Respond with mock protobuf
		var buf []byte
		appendString := func(fieldNum int, val string) {
			tag := byte((fieldNum << 3) | 2)
			buf = append(buf, tag, byte(len(val)))
			buf = append(buf, []byte(val)...)
		}
		appendString(1, "live-api-key-xyz")
		appendString(2, "https://server.codeium.com")
		appendString(4, "https://api.devin.ai")

		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(buf)
	}))
	defer ts.Close()

	ctx := context.Background()
	tokens, err := ExchangeCode(ctx, "test-code", "test-verifier", ts.URL)
	if err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}

	if tokens.APIKey != "live-api-key-xyz" {
		t.Errorf("unexpected APIKey: %s", tokens.APIKey)
	}
	if tokens.DevinAPIURL != "https://api.devin.ai" {
		t.Errorf("unexpected DevinAPIURL: %s", tokens.DevinAPIURL)
	}
}

func TestBuildAuthRecord(t *testing.T) {
	tokens := &TokenResponse{
		APIKey:          "key-123",
		APIServerURL:    "https://server.codeium.com",
		DevinWebappHost: "https://app.devin.ai",
		DevinAPIURL:     "https://api.devin.ai",
		SessionToken:    "token-456",
	}

	auth := BuildAuthRecord(tokens, "abc12345")
	if auth.Provider != "devin" {
		t.Errorf("expected provider devin, got %s", auth.Provider)
	}
	if auth.Attributes["api_key"] != "key-123" {
		t.Errorf("expected api_key key-123, got %s", auth.Attributes["api_key"])
	}
	if auth.Attributes["base_url"] != "https://server.codeium.com" {
		t.Errorf("expected base_url https://server.codeium.com, got %s", auth.Attributes["base_url"])
	}
	if auth.FileName != "devin-abc12345.json" {
		t.Errorf("expected filename devin-abc12345.json, got %s", auth.FileName)
	}
}

func TestBuildAuthRecordDevinCLISessionTokenAndHostFallback(t *testing.T) {
	// Simulate ExchangeDevinCLIPKCECodeResponse where APIKey is empty,
	// APIServerURL was set to "app.devin.ai" (schemeless webapp host)
	tokens := &TokenResponse{
		APIKey:          "",
		SessionToken:    "devin-session-jwt-token-999",
		APIServerURL:    "app.devin.ai",
		DevinWebappHost: "app.devin.ai",
	}

	auth := BuildAuthRecord(tokens, "sess123")
	if auth.Attributes["api_key"] != "devin-session-jwt-token-999" {
		t.Errorf("expected api_key from session_token, got %s", auth.Attributes["api_key"])
	}
	if auth.Attributes["base_url"] != DefaultCodeiumAPIServer {
		t.Errorf("expected base_url fallback to %s, got %s", DefaultCodeiumAPIServer, auth.Attributes["base_url"])
	}
	if auth.Metadata["api_key"] != "devin-session-jwt-token-999" {
		t.Errorf("expected metadata api_key to match session_token, got %v", auth.Metadata["api_key"])
	}
}

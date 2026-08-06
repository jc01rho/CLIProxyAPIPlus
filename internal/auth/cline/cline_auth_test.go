// Package cline_test covers the WorkOS OAuth contract details shared with
// 9Router: wrapper `data` envelope, base64 callback payload, refresh payload
// field names, workos prefix normalization, and expiry parsing.
package cline_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cline"
)

func TestTokenResponse_UnmarshalJSON_WrapperData(t *testing.T) {
	payload := map[string]any{
		"success": true,
		"data": map[string]any{
			"accessToken":  "workos:abc",
			"refreshToken": "rt-1",
			"expiresAt":    "2030-01-02T03:04:05Z",
			"userInfo": map[string]any{
				"email": "user@example.com",
				"id":    "user-id-1",
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var tr cline.TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tr.AccessToken != "workos:abc" {
		t.Errorf("AccessToken = %q, want workos:abc", tr.AccessToken)
	}
	if tr.RefreshToken != "rt-1" {
		t.Errorf("RefreshToken = %q, want rt-1", tr.RefreshToken)
	}
	if tr.UserEmail() != "user@example.com" {
		t.Errorf("UserEmail = %q, want user@example.com", tr.UserEmail())
	}
}

func TestTokenResponse_UnmarshalJSON_Flat(t *testing.T) {
	body := []byte(`{"accessToken":"workos:flat","refreshToken":"rt-flat","expiresAt":"1700000000","email":"flat@example.com"}`)
	var tr cline.TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tr.AccessToken != "workos:flat" {
		t.Errorf("AccessToken = %q", tr.AccessToken)
	}
	if tr.UserEmail() != "flat@example.com" {
		t.Errorf("UserEmail = %q", tr.UserEmail())
	}
}

func TestParseExpiresAt(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{
		{"", 0},
		{"1700000000", 1700000000},
		{"1700000000.5", 1700000000},
		{"2024-01-02T03:04:05Z", 1704164645},
		{"2024-01-02T03:04:05.123456789Z", 1704164645},
	}
	for _, tc := range cases {
		got := cline.ParseExpiresAt(tc.raw)
		if got != tc.want {
			t.Errorf("ParseExpiresAt(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestNormalizeAccessToken(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"plain-token": "workos:plain-token",
		"workos:abc":  "workos:abc",
		"  ":          "",
	}
	for in, want := range cases {
		if got := cline.NormalizeAccessToken(in); got != want {
			t.Errorf("NormalizeAccessToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetBearerHeaderValue(t *testing.T) {
	if got := cline.GetBearerHeaderValue("plain"); got != "Bearer workos:plain" {
		t.Errorf("GetBearerHeaderValue(plain) = %q, want 'Bearer workos:plain'", got)
	}
	if got := cline.GetBearerHeaderValue("workos:abc"); got != "Bearer workos:abc" {
		t.Errorf("GetBearerHeaderValue(workos:abc) = %q", got)
	}
}

func TestShouldRefresh(t *testing.T) {
	if !cline.ShouldRefresh(0) {
		t.Error("ShouldRefresh(0) should be true (unknown expiry)")
	}
	if cline.ShouldRefresh(time.Now().Add(time.Hour).Unix()) {
		t.Error("ShouldRefresh(<future>) should be false")
	}
	if !cline.ShouldRefresh(time.Now().Add(time.Minute).Unix()) {
		t.Error("ShouldRefresh(<near expiry>) should be true")
	}
}

func TestRefreshToken_FieldNames(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("refresh Authorization header should be empty, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"accessToken":"workos:abc","refreshToken":"rt2","expiresAt":"2024-01-02T03:04:05Z","userInfo":{"email":"e@x","id":"uid"}}}`))
	}))
	defer server.Close()

	// The endpoint constants point at api.cline.bot; we rebuild the request
	// manually to assert the wire-level field names without touching the
	// production URL.
	client := server.Client()
	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"grantType":"refresh_token","refreshToken":"rt","clientType":"extension"}`))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if captured["grantType"] != "refresh_token" {
		t.Errorf("captured grantType = %v, want refresh_token", captured["grantType"])
	}
	if captured["clientType"] != "extension" {
		t.Errorf("captured clientType = %v, want extension", captured["clientType"])
	}
	if captured["refreshToken"] != "rt" {
		t.Errorf("captured refreshToken = %v, want rt", captured["refreshToken"])
	}

	// Parse the wrapper response to validate the TokenResponse contract.
	var tr cline.TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if tr.AccessToken != "workos:abc" || tr.UserEmail() != "e@x" {
		t.Errorf("wrapper parse failed: %+v", tr)
	}
}

func TestGenerateAuthURL(t *testing.T) {
	auth := cline.NewClineAuth(nil)
	url := auth.GenerateAuthURL("opaque-state", "http://localhost:1455/callback")
	if !strings.Contains(url, "client_type=extension") {
		t.Errorf("url missing client_type=extension: %s", url)
	}
	if !strings.Contains(url, "callback_url=http%3A%2F%2Flocalhost%3A1455%2Fcallback") && !strings.Contains(url, "callback_url=http://localhost:1455/callback") {
		t.Errorf("url missing callback_url: %s", url)
	}
	if !strings.Contains(url, "redirect_uri=http://localhost:1455/callback") {
		t.Errorf("url missing redirect_uri: %s", url)
	}
	if !strings.Contains(url, "state=opaque-state") {
		t.Errorf("url missing state: %s", url)
	}
}

func TestBase64PaddingDecode(t *testing.T) {
	// Cline delivers the token payload base64-encoded in the `code` callback
	// parameter. Mirror 9Router's parser: pad -> decode -> JSON.parse up to
	// the final `}` to discard trailing noise in the *decoded* bytes.
	payload := `{"accessToken":"workos:base64tok","refreshToken":"rt-base64","expiresAt":"2024-01-02T03:04:05Z","email":"b@x"}trailing-noise-after-json`
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))

	// Pad to a multiple of 4 (StdEncoding already is, but mirror the strategy).
	padding := 4 - (len(encoded) % 4)
	if padding != 4 {
		encoded += strings.Repeat("=", padding)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	s := string(decoded)
	last := strings.LastIndex(s, "}")
	if last == -1 {
		t.Fatalf("no last brace")
	}
	sub := s[:last+1]
	var tr cline.TokenResponse
	if err := json.Unmarshal([]byte(sub), &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tr.AccessToken != "workos:base64tok" {
		t.Errorf("AccessToken = %q", tr.AccessToken)
	}
	if tr.UserEmail() != "b@x" {
		t.Errorf("UserEmail = %q", tr.UserEmail())
	}
}

func TestExchangeCode_9RouterContract(t *testing.T) {
	// 9Router master contract: {grant_type, code, client_type, redirect_uri}
	// with no `provider` key. The exchange endpoint is /api/v1/auth/token.
	var capturedMethod string
	var capturedPath string
	var capturedAuth string
	var capturedCT string
	var capturedUA string
	var capturedReferer string
	var capturedTitle string
	var capturedPayload map[string]any
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		capturedCT = r.Header.Get("Content-Type")
		capturedUA = r.Header.Get("User-Agent")
		capturedReferer = r.Header.Get("HTTP-Referer")
		capturedTitle = r.Header.Get("X-Title")
		if err := json.NewDecoder(r.Body).Decode(&capturedPayload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		// Return wrapper envelope so the UnmarshalJSON path is exercised.
		_, _ = w.Write([]byte(`{"success":true,"data":{"accessToken":"workos:tok","refreshToken":"rt","expiresAt":"2030-01-02T03:04:05Z","userInfo":{"email":"u@example.com","id":"u-1"}}}`))
	}))
	defer tokenSrv.Close()

	auth := cline.NewClineAuth(nil).WithEndpoints(tokenSrv.URL+"/api/v1/auth/token", "", "", "")
	resp, err := auth.ExchangeCode(t.Context(), "auth-code-payload", "http://localhost:1455/callback")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if resp.AccessToken != "workos:tok" {
		t.Errorf("AccessToken = %q, want workos:tok", resp.AccessToken)
	}
	if resp.UserEmail() != "u@example.com" {
		t.Errorf("UserEmail = %q, want u@example.com", resp.UserEmail())
	}
	if capturedMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", capturedMethod)
	}
	if capturedPath != "/api/v1/auth/token" {
		t.Errorf("path = %s, want /api/v1/auth/token", capturedPath)
	}
	if capturedCT != "application/json" {
		t.Errorf("Content-Type = %q", capturedCT)
	}
	if capturedAuth != "" {
		t.Errorf("Authorization should be empty on token exchange, got %q", capturedAuth)
	}
	if !strings.HasPrefix(capturedUA, "Cline/") {
		t.Errorf("User-Agent = %q, want Cline/*", capturedUA)
	}
	if capturedReferer != "https://cline.bot" {
		t.Errorf("HTTP-Referer = %q", capturedReferer)
	}
	if capturedTitle != "Cline" {
		t.Errorf("X-Title = %q", capturedTitle)
	}

	// Wire-format assertions: the 9Router contract is a fixed key set with no
	// provider key. We allow only the four expected fields and nothing else.
	wantKeys := map[string]struct{}{
		"grant_type":   {},
		"code":         {},
		"client_type":  {},
		"redirect_uri": {},
	}
	if len(capturedPayload) != len(wantKeys) {
		t.Fatalf("payload key count = %d (%v), want exactly %d", len(capturedPayload), capturedPayload, len(wantKeys))
	}
	for k := range capturedPayload {
		if _, ok := wantKeys[k]; !ok {
			t.Errorf("payload has unexpected key %q (9Router contract forbids extras)", k)
		}
	}
	if capturedPayload["grant_type"] != "authorization_code" {
		t.Errorf("grant_type = %v, want authorization_code", capturedPayload["grant_type"])
	}
	if capturedPayload["client_type"] != "extension" {
		t.Errorf("client_type = %v, want extension", capturedPayload["client_type"])
	}
	if capturedPayload["code"] != "auth-code-payload" {
		t.Errorf("code = %v", capturedPayload["code"])
	}
	if capturedPayload["redirect_uri"] != "http://localhost:1455/callback" {
		t.Errorf("redirect_uri = %v", capturedPayload["redirect_uri"])
	}
}

func TestRefreshToken_9RouterContract(t *testing.T) {
	// Refresh contract: {grantType, refreshToken, clientType} with no provider key.
	var capturedPayload map[string]any
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&capturedPayload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"accessToken":"workos:rt-tok","refreshToken":"rt-2","expiresAt":"2030-01-02T03:04:05Z","userInfo":{"email":"u@example.com","id":"u-1"}}}`))
	}))
	defer tokenSrv.Close()

	auth := cline.NewClineAuth(nil).WithEndpoints("", tokenSrv.URL+"/api/v1/auth/refresh", "", "")
	resp, err := auth.RefreshToken(t.Context(), "rt-1")
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if resp.AccessToken != "workos:rt-tok" {
		t.Errorf("AccessToken = %q", resp.AccessToken)
	}

	wantKeys := map[string]struct{}{
		"grantType":    {},
		"refreshToken": {},
		"clientType":   {},
	}
	if len(capturedPayload) != len(wantKeys) {
		t.Fatalf("payload key count = %d (%v), want exactly %d", len(capturedPayload), capturedPayload, len(wantKeys))
	}
	for k := range capturedPayload {
		if _, ok := wantKeys[k]; !ok {
			t.Errorf("payload has unexpected key %q (9Router contract forbids extras)", k)
		}
	}
	if capturedPayload["grantType"] != "refresh_token" {
		t.Errorf("grantType = %v", capturedPayload["grantType"])
	}
	if capturedPayload["refreshToken"] != "rt-1" {
		t.Errorf("refreshToken = %v", capturedPayload["refreshToken"])
	}
	if capturedPayload["clientType"] != "extension" {
		t.Errorf("clientType = %v", capturedPayload["clientType"])
	}
}

func TestRefreshToken_NonOKReturnsError(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer tokenSrv.Close()

	auth := cline.NewClineAuth(nil).WithEndpoints("", tokenSrv.URL+"/api/v1/auth/refresh", "", "")
	if _, err := auth.RefreshToken(t.Context(), "rt"); err == nil {
		t.Fatal("RefreshToken should error on non-2xx response")
	}
}

func TestValidateToken_RoundTrip(t *testing.T) {
	// users/me must be reachable via GET with a Bearer workos: header.
	var capturedAuth string
	var capturedMethod string
	var capturedUA string
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedMethod = r.Method
		capturedUA = r.Header.Get("User-Agent")
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"e@x"}`))
	}))
	defer server.Close()

	auth := cline.NewClineAuth(nil).WithEndpoints("", "", server.URL+"/api/v1/users/me", "")
	email, err := auth.ValidateToken(t.Context(), "plain-tok")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if email != "e@x" {
		t.Errorf("email = %q, want e@x", email)
	}
	if capturedMethod != http.MethodGet {
		t.Errorf("method = %s, want GET", capturedMethod)
	}
	if capturedPath != "/api/v1/users/me" {
		t.Errorf("path = %s, want /api/v1/users/me", capturedPath)
	}
	if capturedAuth != "Bearer workos:plain-tok" {
		t.Errorf("Authorization = %q, want Bearer workos:plain-tok", capturedAuth)
	}
	if !strings.HasPrefix(capturedUA, "Cline/") {
		t.Errorf("User-Agent = %q, want Cline/*", capturedUA)
	}

	// Non-2xx surfaces as an error.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer failSrv.Close()
	authFail := cline.NewClineAuth(nil).WithEndpoints("", "", failSrv.URL+"/api/v1/users/me", "")
	if _, err := authFail.ValidateToken(t.Context(), "plain-tok"); err == nil {
		t.Fatal("ValidateToken should error on non-2xx response")
	}
}

func TestValidateToken_DataEnvelopeFallback(t *testing.T) {
	// Some Cline responses wrap identity in a `data` envelope; ValidateToken
	// must read it from there when the top-level email is missing.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"email":"wrapped@x"}}`))
	}))
	defer server.Close()

	auth := cline.NewClineAuth(nil).WithEndpoints("", "", server.URL+"/api/v1/users/me", "")
	email, err := auth.ValidateToken(t.Context(), "tok")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if email != "wrapped@x" {
		t.Errorf("email = %q, want wrapped@x", email)
	}
}

func TestExchangeCode_FlatAndWrapperResponses(t *testing.T) {
	// Both flat and {success, data: {...}} envelopes must round-trip.
	for _, body := range []string{
		`{"accessToken":"workos:flat","refreshToken":"rt","expiresAt":"2024-01-02T03:04:05Z","email":"flat@x"}`,
		`{"success":true,"data":{"accessToken":"workos:wrapped","refreshToken":"rt","expiresAt":"2024-01-02T03:04:05Z","email":"w@x"}}`,
	} {
		tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		auth := cline.NewClineAuth(nil).WithEndpoints(tokenSrv.URL+"/api/v1/auth/token", "", "", "")
		resp, err := auth.ExchangeCode(t.Context(), "code", "http://localhost:1455/callback")
		tokenSrv.Close()
		if err != nil {
			t.Fatalf("body %q: %v", body, err)
		}
		if resp.AccessToken == "" {
			t.Errorf("body %q: AccessToken empty", body)
		}
	}
}

package zcode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// mockZaiServer simulates the Z.AI business API (getCustomerInfo, api_keys).
func mockZaiServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer biz-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"id":    "acct-123",
				"email": "User@Example.com",
				"organizations": []map[string]interface{}{
					{
						"organizationId": "org-1",
						"isDefault":      true,
						"projects": []map[string]interface{}{
							{"projectId": "proj-1", "isDefault": true},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer biz-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"apiKey": "key-1", "name": "zcode-api-key"},
				},
			})
		case http.MethodPost:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{"apiKey": "key-created"},
			})
		}
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys/copy/key-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer biz-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"secretKey": "secret-abc"},
		})
	})
	return httptest.NewServer(mux)
}

// TestGenerateAuthURL verifies the authorize URL shape.
func TestGenerateAuthURL(t *testing.T) {
	o := NewOAuth()
	u := o.GenerateAuthURL("state-1", "zcode://oauth/callback")
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := parsed.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code", q.Get("response_type"))
	}
	if q.Get("client_id") != DefaultClientID {
		t.Errorf("client_id = %q, want %q", q.Get("client_id"), DefaultClientID)
	}
	if q.Get("state") != "state-1" {
		t.Errorf("state = %q, want state-1", q.Get("state"))
	}
	if q.Get("redirect_uri") != "zcode://oauth/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

// TestExchangeCodeFullPipeline verifies the broker -> z/login -> provision
// pipeline produces credentials with the provisioned Z.AI API key.
func TestExchangeCodeFullPipeline(t *testing.T) {
	zai := mockZaiServer(t)
	defer zai.Close()

	// Broker + z/login mock.
	mux := http.NewServeMux()
	mux.HandleFunc("/broker", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["provider"] != "zai" || body["code"] != "auth-code" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"token": "zcode-jwt-token",
				"zai":   map[string]interface{}{"access_token": "upstream-zai-token"},
			},
		})
	})
	mux.HandleFunc("/zlogin", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["token"] != "upstream-zai-token" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"access_token": "biz-token"},
		})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	o := NewOAuthWithConfig(Config{
		BrokerTokenURL: svc.URL + "/broker",
		ZaiLoginURL:    svc.URL + "/zlogin",
		ZaiAPIBase:     zai.URL,
	})

	creds, err := o.ExchangeCode(context.Background(), "auth-code", "state-1", "zcode://oauth/callback")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if creds.AccessToken != "key-1.secret-abc" {
		t.Errorf("AccessToken = %q, want key-1.secret-abc", creds.AccessToken)
	}
	if creds.RefreshToken != "upstream-zai-token" {
		t.Errorf("RefreshToken = %q, want upstream-zai-token", creds.RefreshToken)
	}
	if creds.Email != "user@example.com" {
		t.Errorf("Email = %q, want user@example.com", creds.Email)
	}
	if creds.AccountID != "acct-123" {
		t.Errorf("AccountID = %q, want acct-123", creds.AccountID)
	}
	if !creds.ExpiresAt.After(time.Now().Add(9 * 365 * 24 * time.Hour)) {
		t.Errorf("ExpiresAt = %v, want ~10y in future", creds.ExpiresAt)
	}
}

// TestRefreshReProvisions verifies Refresh re-provisions from the upstream token.
func TestRefreshReProvisions(t *testing.T) {
	zai := mockZaiServer(t)
	defer zai.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/zlogin", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"access_token": "biz-token"},
		})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	o := NewOAuthWithConfig(Config{
		ZaiLoginURL: svc.URL + "/zlogin",
		ZaiAPIBase:  zai.URL,
	})

	creds, err := o.Refresh(context.Background(), &Credentials{RefreshToken: "upstream-zai-token"})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if creds.AccessToken != "key-1.secret-abc" {
		t.Errorf("AccessToken = %q, want key-1.secret-abc", creds.AccessToken)
	}
}

// TestRefreshNoUpstreamToken verifies Refresh fails loudly without a stored token.
func TestRefreshNoUpstreamToken(t *testing.T) {
	o := NewOAuth()
	_, err := o.Refresh(context.Background(), &Credentials{})
	if err == nil {
		t.Fatal("expected error for missing upstream token")
	}
	if !strings.Contains(err.Error(), "re-login") {
		t.Errorf("error = %q, want re-login hint", err.Error())
	}
}

// TestRedactSecrets verifies token-like substrings are masked in errors.
func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("error with eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U and aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if strings.Contains(got, "eyJ") {
		t.Errorf("JWT not redacted: %q", got)
	}
	if strings.Contains(got, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("long token not redacted: %q", got)
	}
}

// TestProvisionFromUpstreamNumericIDs verifies provisioning works when the
// Z.AI API encodes IDs as JSON numbers and z/login returns a camelCase token,
// mirroring the reported "cannot unmarshal number into ... data.id" failure.
func TestProvisionFromUpstreamNumericIDs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zlogin", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"accessToken": "biz-token"}, // camelCase
		})
	})
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer biz-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"id":    123456, // numeric id
				"email": "User@Example.com",
				"organizations": []map[string]interface{}{
					{
						"organizationId":   12345, // numeric organizationId
						"organizationName": "我的默认机构",
						"projects": []map[string]interface{}{
							{"projectId": 67890, "projectName": "默认项目"}, // numeric projectId
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/12345/projects/67890/api_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer biz-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"apiKey": "key-1", "name": "zcode-api-key"},
				},
			})
		case http.MethodPost:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{"apiKey": "key-created"},
			})
		}
	})
	mux.HandleFunc("/api/biz/v1/organization/12345/projects/67890/api_keys/copy/key-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer biz-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"secretKey": "secret-abc"},
		})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	o := NewOAuthWithConfig(Config{
		ZaiLoginURL: svc.URL + "/zlogin",
		ZaiAPIBase:  svc.URL,
	})

	creds, err := o.ProvisionFromUpstream(context.Background(), "upstream-zai-token", "zcode-jwt")
	if err != nil {
		t.Fatalf("ProvisionFromUpstream: %v", err)
	}
	if creds.AccessToken != "key-1.secret-abc" {
		t.Errorf("AccessToken = %q, want key-1.secret-abc", creds.AccessToken)
	}
	if creds.Email != "user@example.com" {
		t.Errorf("Email = %q, want user@example.com", creds.Email)
	}
	if creds.AccountID != "123456" {
		t.Errorf("AccountID = %q, want 123456", creds.AccountID)
	}
}

// --- gjc parity regression tests (RED before the gjc migration) ---

// mockZaiRealisticServer simulates the REAL Z.AI business API failure shape the
// user hit in production: an unauthenticated api_keys.create returns HTTP 200
// with an error body and NO data field (not a 401), which is why a bearer-less
// create silently yields "missing apiKey id". It also serves org/project and
// copy responses WITHOUT the "data" wrapper, matching the reference
// implementation's payload.data ?? payload handling.
type realisticZaiServer struct {
	createAuthSeen string
	createCalled   bool
}

func newRealisticZaiServer(t *testing.T, unwrapped bool) (*httptest.Server, *realisticZaiServer) {
	t.Helper()
	state := &realisticZaiServer{}
	mux := http.NewServeMux()
	// wrap emits {"data": v} normally, or bare v when unwrapped is set.
	wrap := func(w http.ResponseWriter, v interface{}) {
		if unwrapped {
			json.NewEncoder(w).Encode(v)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"data": v})
	}
	mux.HandleFunc("/zlogin", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"access_token": "biz-token"},
		})
	})
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer biz-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		wrap(w, map[string]interface{}{
			"id":    "acct-123",
			"email": "User@Example.com",
			"organizations": []map[string]interface{}{
				{
					"organizationId": "org-1",
					"isDefault":      true,
					"projects": []map[string]interface{}{
						{"projectId": "proj-1", "isDefault": true},
					},
				},
			},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.Header.Get("Authorization") != "Bearer biz-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// No existing "zcode-api-key" -> the flow must create one.
			json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]interface{}{}})
		case http.MethodPost:
			state.createCalled = true
			state.createAuthSeen = r.Header.Get("Authorization")
			if state.createAuthSeen != "Bearer biz-token" {
				// REAL Z.AI behavior: HTTP 200 with an error body and no data.
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"code": 401, "message": "unauthorized", "success": false,
				})
				return
			}
			wrap(w, map[string]interface{}{"apiKey": "key-created"})
		}
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys/copy/key-created", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer biz-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		wrap(w, map[string]interface{}{"secretKey": "secret-xyz"})
	})
	return httptest.NewServer(mux), state
}

// TestProvisionSendsBearerOnAPIKeyCreate pins the gjc reference behavior that
// api_keys.create carries the business-token bearer. Without it, the real Z.AI
// API answers HTTP 200 with an error body and provisioning dies with
// "missing apiKey id" — the exact production failure.
func TestProvisionSendsBearerOnAPIKeyCreate(t *testing.T) {
	srv, state := newRealisticZaiServer(t, false)
	defer srv.Close()

	o := NewOAuthWithConfig(Config{ZaiLoginURL: srv.URL + "/zlogin", ZaiAPIBase: srv.URL})
	creds, err := o.ProvisionFromUpstream(context.Background(), "upstream-zai-token", "")
	if err != nil {
		t.Fatalf("ProvisionFromUpstream: %v", err)
	}
	if !state.createCalled {
		t.Fatal("api_keys.create was never called")
	}
	if state.createAuthSeen != "Bearer biz-token" {
		t.Errorf("api_keys.create Authorization = %q, want %q", state.createAuthSeen, "Bearer biz-token")
	}
	if creds.AccessToken != "key-created.secret-xyz" {
		t.Errorf("AccessToken = %q, want key-created.secret-xyz", creds.AccessToken)
	}
}

// TestProvisionAcceptsUnwrappedPayloads pins the reference's
// payload.data ?? payload fallback: getCustomerInfo, api_keys.create, and
// api_keys.copy must work when the response omits the "data" wrapper.
func TestProvisionAcceptsUnwrappedPayloads(t *testing.T) {
	srv, _ := newRealisticZaiServer(t, true)
	defer srv.Close()

	o := NewOAuthWithConfig(Config{ZaiLoginURL: srv.URL + "/zlogin", ZaiAPIBase: srv.URL})
	creds, err := o.ProvisionFromUpstream(context.Background(), "upstream-zai-token", "")
	if err != nil {
		t.Fatalf("ProvisionFromUpstream with unwrapped payloads: %v", err)
	}
	if creds.AccessToken != "key-created.secret-xyz" {
		t.Errorf("AccessToken = %q, want key-created.secret-xyz", creds.AccessToken)
	}
	if creds.Email != "user@example.com" {
		t.Errorf("Email = %q, want user@example.com", creds.Email)
	}
}

// TestParseCallbackInput pins the reference parseCallbackInput contract: a
// pasted zcode:// redirect URL, a bare query string, and a raw code all yield
// the authorization code.
func TestParseCallbackInput(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantCode  string
		wantState string
	}{
		{"full zcode redirect url", "zcode://oauth/callback?code=abc123&state=st-1", "abc123", "st-1"},
		{"query string only", "?code=abc123&state=st-1", "abc123", "st-1"},
		{"bare code", "abc123", "abc123", ""},
		{"bare code with fragment state", "abc123#st-1", "abc123", "st-1"},
		{"whitespace padded url", "  zcode://oauth/callback?code=abc123&state=st-1  ", "abc123", "st-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, state := ParseCallbackInput(tc.input)
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
		})
	}
}

// TestExchangeCodeAcceptsPastedRedirectURL pins that ExchangeCode extracts the
// code from a pasted zcode:// redirect URL, mirroring the reference flow where
// the CLI cannot catch the custom-protocol redirect.
func TestExchangeCodeAcceptsPastedRedirectURL(t *testing.T) {
	zai := mockZaiServer(t)
	defer zai.Close()

	var brokerCode string
	mux := http.NewServeMux()
	mux.HandleFunc("/broker", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		brokerCode = body["code"]
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"token": "zcode-jwt-token",
				"zai":   map[string]interface{}{"access_token": "upstream-zai-token"},
			},
		})
	})
	mux.HandleFunc("/zlogin", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"access_token": "biz-token"},
		})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	o := NewOAuthWithConfig(Config{
		BrokerTokenURL: svc.URL + "/broker",
		ZaiLoginURL:    svc.URL + "/zlogin",
		ZaiAPIBase:     zai.URL,
	})

	creds, err := o.ExchangeCode(context.Background(), "zcode://oauth/callback?code=pasted-code&state=state-1", "state-1", "")
	if err != nil {
		t.Fatalf("ExchangeCode with pasted redirect URL: %v", err)
	}
	if brokerCode != "pasted-code" {
		t.Errorf("broker received code = %q, want pasted-code", brokerCode)
	}
	if creds.AccessToken != "key-1.secret-abc" {
		t.Errorf("AccessToken = %q, want key-1.secret-abc", creds.AccessToken)
	}
}

// TestOrgProjectSelectionPrefersFirstDefault pins the reference selection
// semantics: the FIRST organization/project flagged isDefault wins (falling
// back to the first entry), with no name-based matching.
func TestOrgProjectSelectionPrefersFirstDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zlogin", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"access_token": "biz-token"},
		})
	})
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"id":    "acct-123",
				"email": "user@example.com",
				"organizations": []map[string]interface{}{
					{
						"organizationId": "org-first-default",
						"isDefault":      true,
						"projects": []map[string]interface{}{
							{"projectId": "proj-first-default", "isDefault": true},
							{"projectId": "proj-second-default", "isDefault": true},
						},
					},
					{
						"organizationId":   "org-second-default",
						"organizationName": "默认机构",
						"isDefault":        true,
						"projects": []map[string]interface{}{
							{"projectId": "proj-other", "isDefault": true},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-first-default/projects/proj-first-default/api_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{{"apiKey": "key-ok", "name": "zcode-api-key"}},
			})
		}
	})
	mux.HandleFunc("/api/biz/v1/organization/org-first-default/projects/proj-first-default/api_keys/copy/key-ok", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"secretKey": "secret-ok"},
		})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	o := NewOAuthWithConfig(Config{ZaiLoginURL: svc.URL + "/zlogin", ZaiAPIBase: svc.URL})
	creds, err := o.ProvisionFromUpstream(context.Background(), "upstream-zai-token", "")
	if err != nil {
		t.Fatalf("ProvisionFromUpstream: %v", err)
	}
	if creds.AccessToken != "key-ok.secret-ok" {
		t.Errorf("AccessToken = %q, want key-ok.secret-ok (first isDefault org/project)", creds.AccessToken)
	}
}

// TestProvisionFromUpstreamDoesNotPersistBrokerJWT pins the gajae-code
// contract: the broker JWT is consumed only while resolving identity and is
// never stored on the credentials.
//
// gajae-code credentialsFromApiKey (packages/ai/src/utils/oauth/glm-zcode.ts)
// builds {access, refresh, expires, email, accountId} — its OAuthCredentials
// type has no token field at all — and provisionFromUpstream passes the JWT in
// only as zcodeTokenForIdentity. Persisting it here is what let the removed
// start-plan gateway routing read a credential the reference never keeps.
func TestProvisionFromUpstreamDoesNotPersistBrokerJWT(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"id":    "acct-1",
				"email": "user@example.com",
				"organizations": []map[string]interface{}{
					{
						"organizationId": "org-1",
						"isDefault":      true,
						"projects": []map[string]interface{}{
							{"projectId": "proj-1", "isDefault": true},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/zlogin", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"access_token": "biz-token"},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{{"apiKey": "key-ok", "name": "zcode-api-key"}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"apiKey": "key-new"},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys/copy/key-ok", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"secretKey": "secret-ok"},
		})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	cfg := Config{
		ZaiLoginURL: svc.URL + "/zlogin",
		ZaiAPIBase:  svc.URL,
	}
	o := NewOAuthWithConfig(cfg)
	creds, err := o.ProvisionFromUpstream(context.Background(), "upstream-token", "jwt-stored")
	if err != nil {
		t.Fatalf("ProvisionFromUpstream: %v", err)
	}
	if creds.AccessToken != "key-ok.secret-ok" {
		t.Errorf("AccessToken = %q, want key-ok.secret-ok", creds.AccessToken)
	}

	// Refresh re-provisions without any stored broker JWT to carry.
	creds2, err := o.Refresh(context.Background(), creds)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if creds2.AccessToken != "key-ok.secret-ok" {
		t.Errorf("Refresh AccessToken = %q, want key-ok.secret-ok", creds2.AccessToken)
	}
	if creds2.RefreshToken != "upstream-token" {
		t.Errorf("Refresh dropped upstream token: %q", creds2.RefreshToken)
	}
}

// TestProvisionFromUpstreamResolvesIdentityFromBrokerJWT verifies the broker JWT
// is still consumed as an identity source when the account endpoints carry
// none, matching gajae-code resolveIdentity's
// [zcodeTokenForIdentity, businessToken] candidate order.
func TestProvisionFromUpstreamResolvesIdentityFromBrokerJWT(t *testing.T) {
	mux := http.NewServeMux()
	// No id/email here, so identity must come from a JWT candidate.
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"organizations": []map[string]interface{}{
					{
						"organizationId": "org-1",
						"isDefault":      true,
						"projects": []map[string]interface{}{
							{"projectId": "proj-1", "isDefault": true},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/zlogin", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"access_token": "biz-token"},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"apiKey": "key-ok", "name": "zcode-api-key"}},
		})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys/copy/key-ok", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"secretKey": "secret-ok"},
		})
	})
	// userinfo carries nothing usable, forcing the JWT fallback.
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{}})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	o := NewOAuthWithConfig(Config{
		ZaiLoginURL: svc.URL + "/zlogin",
		ZaiAPIBase:  svc.URL,
		UserinfoURL: svc.URL + "/userinfo",
	})

	// {"sub":"acct-jwt","email":"Broker@Example.com"}
	brokerJWT := "eyJhbGciOiJIUzI1NiJ9." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"acct-jwt","email":"Broker@Example.com"}`)) +
		".sig"

	creds, err := o.ProvisionFromUpstream(context.Background(), "upstream-token", brokerJWT)
	if err != nil {
		t.Fatalf("ProvisionFromUpstream: %v", err)
	}
	if creds.AccountID != "acct-jwt" {
		t.Errorf("AccountID = %q, want acct-jwt (resolved from the broker JWT)", creds.AccountID)
	}
	if creds.Email != "broker@example.com" {
		t.Errorf("Email = %q, want broker@example.com (lowercased from the broker JWT)", creds.Email)
	}
}

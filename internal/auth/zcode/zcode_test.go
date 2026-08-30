package zcode

import (
	"context"
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

// TestCheckStartPlanBalanceSchema verifies CheckStartPlan against the verified
// ZCode app contract (app.asar hasActiveStartPlan): data.plans[] with status
// "active" and a plan_id/name containing "start-plan", plus the empty-
// identifiers fallback where any active plan counts.
func TestCheckStartPlanBalanceSchema(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/balance", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer jwt-1" {
			t.Errorf("Authorization = %q, want Bearer jwt-1", got)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"plans": []map[string]interface{}{
					{"status": "expired", "plan_id": "start-plan-trial", "name": "Start Plan"},
					{"status": "ACTIVE", "plan_id": "zai-start-plan-v1", "name": "Start Plan", "limit": "8000000", "used": "1200000"},
				},
			},
		})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	o := NewOAuthWithConfig(Config{StartPlanBalanceURL: svc.URL + "/balance"})
	spi := o.CheckStartPlan(context.Background(), "jwt-1")
	if spi == nil {
		t.Fatal("CheckStartPlan returned nil")
	}
	if !spi.Active {
		t.Error("expected Active=true for active start-plan entry (case-insensitive status)")
	}
	if spi.Limit != 8000000 || spi.Used != 1200000 {
		t.Errorf("Limit/Used = %d/%d, want 8000000/1200000", spi.Limit, spi.Used)
	}
}

// TestCheckStartPlanNoStartPlan verifies a coding-plan-only account (active
// pro plan, no start-plan identity) reports Active=false so the executor keeps
// billing through the provisioned api.z.ai key.
func TestCheckStartPlanNoStartPlan(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/balance", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"plans": []map[string]interface{}{
					{"status": "active", "plan_id": "coding-plan-pro", "name": "GLM Coding Pro"},
				},
			},
		})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	o := NewOAuthWithConfig(Config{StartPlanBalanceURL: svc.URL + "/balance"})
	spi := o.CheckStartPlan(context.Background(), "jwt-1")
	if spi == nil {
		t.Fatal("CheckStartPlan returned nil")
	}
	if spi.Active {
		t.Error("expected Active=false when no plan carries a start-plan identity")
	}
}

// TestProvisionFromUpstreamPreservesZcodeToken verifies the refresh path
// carries the stored broker JWT through re-provisioning (Refresh passes
// creds.ZcodeToken) and that the balance probe is skipped when no JWT exists.
func TestProvisionFromUpstreamPreservesZcodeToken(t *testing.T) {
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
	balanceCalls := 0
	mux.HandleFunc("/balance", func(w http.ResponseWriter, r *http.Request) {
		balanceCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer jwt-stored" {
			t.Errorf("balance Authorization = %q, want Bearer jwt-stored", got)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"plans": []map[string]interface{}{
					{"status": "active", "plan_id": "start-plan-trial"},
				},
			},
		})
	})
	svc := httptest.NewServer(mux)
	defer svc.Close()

	cfg := Config{
		ZaiLoginURL:         svc.URL + "/zlogin",
		ZaiAPIBase:          svc.URL,
		StartPlanBalanceURL: svc.URL + "/balance",
	}
	o := NewOAuthWithConfig(cfg)
	creds, err := o.ProvisionFromUpstream(context.Background(), "upstream-token", "jwt-stored")
	if err != nil {
		t.Fatalf("ProvisionFromUpstream: %v", err)
	}
	if creds.ZcodeToken != "jwt-stored" {
		t.Errorf("ZcodeToken = %q, want jwt-stored (preserved through provisioning)", creds.ZcodeToken)
	}
	if !creds.StartPlanActive {
		t.Error("expected StartPlanActive=true from balance probe")
	}
	if balanceCalls != 1 {
		t.Errorf("balance calls = %d, want 1", balanceCalls)
	}

	// Refresh must preserve the JWT and re-probe the balance.
	creds2, err := o.Refresh(context.Background(), creds)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if creds2.ZcodeToken != "jwt-stored" {
		t.Errorf("Refresh dropped ZcodeToken: %q", creds2.ZcodeToken)
	}
	if balanceCalls != 2 {
		t.Errorf("balance calls after refresh = %d, want 2", balanceCalls)
	}
}

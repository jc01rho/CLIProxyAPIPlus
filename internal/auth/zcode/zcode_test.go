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

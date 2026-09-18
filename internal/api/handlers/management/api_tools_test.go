package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPICallUsesRequestProxyURL(t *testing.T) {
	t.Parallel()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxyServer.Close()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:1"},
		},
	}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"method":"GET","url":"http://upstream.invalid/test","proxy_url":"` + proxyServer.URL + `"}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response apiCallResponse
	if errDecode := json.NewDecoder(recorder.Body).Decode(&response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upstream status code = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if response.Body != "proxied" {
		t.Fatalf("upstream body = %q, want %q", response.Body, "proxied")
	}
}

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"}, "")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"}, "")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportRequestProxyOverridesCredentialAndGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, " http://request-proxy.example.com:8080 ")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://request-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportInvalidRequestProxyDoesNotFallBack(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, "bad-value")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected invalid request proxy to avoid lower-priority proxy settings")
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			XAIKey: []config.XAIKey{{
				APIKey:   "xai-key",
				ProxyURL: "http://xai-proxy.example.com:8080",
			}},
			MetaKey: []config.MetaKey{{
				APIKey:   "meta-key",
				ProxyURL: "http://meta-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "xai",
			auth: &coreauth.Auth{
				Provider:   "xai",
				Attributes: map[string]string{"api_key": "xai-key"},
			},
			wantProxy: "http://xai-proxy.example.com:8080",
		},
		{
			name: "meta",
			auth: &coreauth.Auth{
				Provider:   "meta",
				Attributes: map[string]string{"api_key": "meta-key"},
			},
			wantProxy: "http://meta-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth, "")
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}

func TestAPICallReplacesTokenInBodyData(t *testing.T) {
	t.Parallel()

	var receivedBody string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstreamServer.Close()

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	devinAuth := &coreauth.Auth{
		ID:       "devin-test.json",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key": "secret-session-token-xyz",
		},
		Metadata: map[string]any{
			"type":    "devin",
			"api_key": "secret-session-token-xyz",
		},
	}
	if _, errRegister := manager.Register(context.Background(), devinAuth); errRegister != nil {
		t.Fatalf("register devin auth: %v", errRegister)
	}
	authIndex := devinAuth.EnsureIndex()

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	router := gin.New()
	router.POST("/", h.APICall)

	reqPayload := map[string]any{
		"method":     "POST",
		"url":        upstreamServer.URL,
		"auth_index": authIndex,
		"header": map[string]string{
			"Content-Type": "application/json",
		},
		"data": `{"metadata":{"apiKey":"$TOKEN$","ideName":"chisel"}}`,
	}
	reqBytes, _ := json.Marshal(reqPayload)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(reqBytes)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	expectedBody := `{"metadata":{"apiKey":"secret-session-token-xyz","ideName":"chisel"}}`
	if receivedBody != expectedBody {
		t.Fatalf("received body = %q, want %q", receivedBody, expectedBody)
	}
}

func TestAPICallRejectsUnresolvedTokenPlaceholder(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		body    map[string]any
		wantErr []string
	}{
		{
			name: "omitted auth_index in header",
			body: map[string]any{
				"method": "GET",
				"header": map[string]string{"Authorization": "Bearer $TOKEN$"},
			},
			wantErr: []string{"auth token not found"},
		},
		{
			name: "omitted auth_index in data",
			body: map[string]any{
				"method": "POST",
				"data":   `{"apiKey":"$TOKEN$"}`,
			},
			wantErr: []string{"auth token not found"},
		},
		{
			name: "unmatched auth_index",
			body: map[string]any{
				"auth_index": "missing-auth-index",
				"authIndex":  "missing-auth-index",
				"method":     "GET",
				"header":     map[string]string{"Authorization": "Bearer $TOKEN$"},
			},
			wantErr: []string{"auth token not found", "auth credential not found for auth_index"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var upstreamHit atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				upstreamHit.Store(true)
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			manager := coreauth.NewManager(nil, nil, nil)
			h := &Handler{cfg: &config.Config{}, authManager: manager}
			router := gin.New()
			router.POST("/", h.APICall)

			payload := map[string]any{}
			for k, v := range tc.body {
				payload[k] = v
			}
			payload["url"] = upstream.URL + "/test"
			bodyBytes, errMarshal := json.Marshal(payload)
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}

			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(bodyBytes)))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			var errBody map[string]any
			if errDecode := json.Unmarshal(recorder.Body.Bytes(), &errBody); errDecode != nil {
				t.Fatalf("decode error body: %v; body = %s", errDecode, recorder.Body.String())
			}
			gotErr, _ := errBody["error"].(string)
			matched := false
			for _, want := range tc.wantErr {
				if gotErr == want {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("error = %q, want one of %q", gotErr, tc.wantErr)
			}
			if upstreamHit.Load() {
				t.Fatal("upstream must not receive a request when $TOKEN$ cannot be resolved")
			}
		})
	}
}

func TestAPICallReplacesXAIOAuthAccessToken(t *testing.T) {
	t.Parallel()

	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	xaiAuth := &coreauth.Auth{
		ID:       "xai-user@example.com.json",
		FileName: "xai-user@example.com.json",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.x.ai/v1",
		},
		Metadata: map[string]any{
			"type":          "xai",
			"auth_kind":     "oauth",
			"access_token":  "xai-oauth-access-token",
			"refresh_token": "xai-refresh-token",
			"base_url":      "https://api.x.ai/v1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
		t.Fatalf("register xai auth: %v", errRegister)
	}
	authIndex := xaiAuth.EnsureIndex()

	h := &Handler{cfg: &config.Config{}, authManager: manager}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"authIndex":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `/billing","header":{"Authorization":"Bearer $TOKEN$","x-xai-token-auth":"xai-grok-cli"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if receivedAuth != "Bearer xai-oauth-access-token" {
		t.Fatalf("Authorization = %q, want %q", receivedAuth, "Bearer xai-oauth-access-token")
	}
}

func TestAPICallRefreshesExpiredXAIOAuthToken(t *testing.T) {
	t.Parallel()

	var refreshCalls atomic.Int32
	var refreshMethod, refreshGrantType, refreshToken string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		refreshMethod = r.Method
		_ = r.ParseForm()
		refreshGrantType = r.PostForm.Get("grant_type")
		refreshToken = r.PostForm.Get("refresh_token")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "xai-access-fresh",
			"refresh_token": "xai-refresh-new",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	xaiAuth := &coreauth.Auth{
		ID:       "xai-expired@example.com.json",
		FileName: "xai-expired@example.com.json",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.x.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "xai",
			"auth_kind":      "oauth",
			"access_token":   "xai-access-stale",
			"refresh_token":  "xai-refresh-old",
			"token_endpoint": tokenServer.URL,
			"base_url":       "https://api.x.ai/v1",
			"expired":        time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
		t.Fatalf("register xai auth: %v", errRegister)
	}
	authIndex := xaiAuth.EnsureIndex()

	h := &Handler{cfg: &config.Config{}, authManager: manager, tokenStore: store}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"auth_index":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `/billing","header":{"Authorization":"Bearer $TOKEN$"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if receivedAuth != "Bearer xai-access-fresh" {
		t.Fatalf("Authorization = %q, want %q", receivedAuth, "Bearer xai-access-fresh")
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls.Load())
	}
	if refreshMethod != http.MethodPost {
		t.Fatalf("refresh method = %s, want POST", refreshMethod)
	}
	if refreshGrantType != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", refreshGrantType)
	}
	if refreshToken != "xai-refresh-old" {
		t.Fatalf("refresh_token = %q, want xai-refresh-old", refreshToken)
	}
	live := h.authByIndex(authIndex)
	if live == nil {
		t.Fatal("expected refreshed auth to remain addressable by auth_index")
	}
	if got, _ := live.Metadata["access_token"].(string); got != "xai-access-fresh" {
		t.Fatalf("manager access_token = %q, want xai-access-fresh", got)
	}
	if got, _ := live.Metadata["refresh_token"].(string); got != "xai-refresh-new" {
		t.Fatalf("manager refresh_token = %q, want xai-refresh-new", got)
	}
	saved := store.items[xaiAuth.ID]
	if saved == nil {
		t.Fatal("expected refreshed auth to be persisted to token store")
	}
	if got, _ := saved.Metadata["access_token"].(string); got != "xai-access-fresh" {
		t.Fatalf("persisted access_token = %q, want xai-access-fresh", got)
	}
}

func TestAPICallUsesXAITokenStorageWithoutRefresh(t *testing.T) {
	t.Parallel()

	var tokenEndpointHit atomic.Bool
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenEndpointHit.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tokenServer.Close()

	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	xaiAuth := &coreauth.Auth{
		ID:       "xai-storage-only@example.com.json",
		FileName: "xai-storage-only@example.com.json",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.x.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "xai",
			"auth_kind":      "oauth",
			"token_endpoint": tokenServer.URL,
			"refresh_token":  "xai-refresh-token",
			"base_url":       "https://api.x.ai/v1",
		},
		Storage: &xaiauth.TokenStorage{
			Type:          "xai",
			AuthKind:      "oauth",
			AccessToken:   "xai-valid-storage-token",
			RefreshToken:  "xai-refresh-token",
			TokenEndpoint: tokenServer.URL,
			Expire:        time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
		t.Fatalf("register xai auth: %v", errRegister)
	}
	authIndex := xaiAuth.EnsureIndex()

	h := &Handler{cfg: &config.Config{}, authManager: manager}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"authIndex":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `/billing","header":{"Authorization":"Bearer $TOKEN$"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if receivedAuth != "Bearer xai-valid-storage-token" {
		t.Fatalf("Authorization = %q, want %q", receivedAuth, "Bearer xai-valid-storage-token")
	}
	if tokenEndpointHit.Load() {
		t.Fatal("valid token in storage must not trigger refresh")
	}
}

func TestAPICallRefreshesExpiredXAIOAuthToken_RefreshFailure(t *testing.T) {
	t.Parallel()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer tokenServer.Close()

	var upstreamHit atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	xaiAuth := &coreauth.Auth{
		ID:       "xai-failed-refresh@example.com.json",
		FileName: "xai-failed-refresh@example.com.json",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.x.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "xai",
			"auth_kind":      "oauth",
			"access_token":   "xai-expired-token",
			"refresh_token":  "xai-bad-refresh",
			"token_endpoint": tokenServer.URL,
			"expired":        time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
		t.Fatalf("register xai auth: %v", errRegister)
	}
	authIndex := xaiAuth.EnsureIndex()

	h := &Handler{cfg: &config.Config{}, authManager: manager}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"auth_index":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `/billing","header":{"Authorization":"Bearer $TOKEN$"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	var respBody map[string]any
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &respBody); errDecode != nil {
		t.Fatalf("decode error body: %v", errDecode)
	}
	if got, _ := respBody["error"].(string); got != "auth token refresh failed" {
		t.Fatalf("error = %q, want 'auth token refresh failed'", got)
	}
	if upstreamHit.Load() {
		t.Fatal("upstream must not be hit when token refresh fails")
	}
}

func TestAPICallEndToEndWithAuthFilesList(t *testing.T) {
	tempDir, errTemp := os.MkdirTemp("", "xai-auth-e2e-*")
	if errTemp != nil {
		t.Fatal(errTemp)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	authJSON := `{
  "type": "xai",
  "auth_kind": "oauth",
  "base_url": "https://api.x.ai/v1",
  "access_token": "xai-e2e-access-token",
  "refresh_token": "xai-e2e-refresh-token",
  "id_token": "xai-e2e-id-token",
  "email": "user@example.com",
  "disabled": false
}`
	filePath := filepath.Join(tempDir, "xai-user.json")
	if errWrite := os.WriteFile(filePath, []byte(authJSON), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}

	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(tempDir)

	manager := coreauth.NewManager(store, nil, nil)
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatal(errLoad)
	}

	cfg := &config.Config{AuthDir: tempDir}
	h := &Handler{cfg: cfg, authManager: manager, tokenStore: store}
	router := gin.New()
	router.GET("/v0/management/auth-files", h.ListAuthFiles)
	router.POST("/v0/management/api-call", h.APICall)

	recList := httptest.NewRecorder()
	reqList := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	router.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("ListAuthFiles status = %d, want 200; body = %s", recList.Code, recList.Body.String())
	}

	var listData struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(recList.Body.Bytes(), &listData); errDecode != nil {
		t.Fatalf("decode auth-files: %v", errDecode)
	}
	if len(listData.Files) == 0 {
		t.Fatal("expected at least one auth file in list")
	}

	authIndex, _ := listData.Files[0]["auth_index"].(string)
	if strings.TrimSpace(authIndex) == "" {
		t.Fatal("expected non-empty auth_index from GET /v0/management/auth-files")
	}

	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"billing":"ok"}`))
	}))
	defer upstream.Close()

	callPayload := map[string]any{
		"authIndex": authIndex,
		"method":    "GET",
		"url":       upstream.URL + "/v1/billing?format=credits",
		"header": map[string]string{
			"Authorization":    "Bearer $TOKEN$",
			"x-xai-token-auth": "xai-grok-cli",
		},
	}
	callBytes, _ := json.Marshal(callPayload)

	recCall := httptest.NewRecorder()
	reqCall := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(string(callBytes)))
	reqCall.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recCall, reqCall)

	if recCall.Code != http.StatusOK {
		t.Fatalf("APICall status = %d, want 200; body = %s", recCall.Code, recCall.Body.String())
	}
	if receivedAuth != "Bearer xai-e2e-access-token" {
		t.Fatalf("upstream Authorization = %q, want 'Bearer xai-e2e-access-token'", receivedAuth)
	}
}

func TestAPICallPrioritizesStorageAccessTokenOverMetadataIDToken(t *testing.T) {
	t.Parallel()

	var tokenEndpointHit atomic.Bool
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenEndpointHit.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tokenServer.Close()

	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	xaiAuth := &coreauth.Auth{
		ID:       "xai-storage-pref@example.com.json",
		FileName: "xai-storage-pref@example.com.json",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.x.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "xai",
			"auth_kind":      "oauth",
			"id_token":       "xai-raw-id-token",
			"token_endpoint": tokenServer.URL,
			"refresh_token":  "xai-refresh-token",
		},
		Storage: &xaiauth.TokenStorage{
			Type:          "xai",
			AuthKind:      "oauth",
			AccessToken:   "xai-real-access-token",
			RefreshToken:  "xai-refresh-token",
			TokenEndpoint: tokenServer.URL,
			Expire:        time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
		t.Fatalf("register xai auth: %v", errRegister)
	}
	authIndex := xaiAuth.EnsureIndex()

	h := &Handler{cfg: &config.Config{}, authManager: manager}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"authIndex":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `/billing","header":{"Authorization":"Bearer $TOKEN$"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if receivedAuth != "Bearer xai-real-access-token" {
		t.Fatalf("Authorization = %q, want 'Bearer xai-real-access-token' (must not use id_token)", receivedAuth)
	}
	if tokenEndpointHit.Load() {
		t.Fatal("token endpoint must not be hit when storage access token is valid")
	}
}

func TestAPICallConcurrentXAITokenRefresh(t *testing.T) {
	t.Parallel()

	var refreshCount atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "xai-fresh-concurrent",
			"refresh_token": "xai-new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer xai-fresh-concurrent" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`invalid auth`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	xaiAuth := &coreauth.Auth{
		ID:       "xai-concurrent@example.com.json",
		FileName: "xai-concurrent@example.com.json",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.x.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "xai",
			"auth_kind":      "oauth",
			"access_token":   "xai-stale-concurrent",
			"refresh_token":  "xai-refresh-shared",
			"token_endpoint": tokenServer.URL,
			"expired":        time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
		Storage: &xaiauth.TokenStorage{
			Type:          "xai",
			AuthKind:      "oauth",
			AccessToken:   "xai-stale-concurrent",
			RefreshToken:  "xai-refresh-shared",
			TokenEndpoint: tokenServer.URL,
			Expire:        time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
		t.Fatalf("register xai auth: %v", errRegister)
	}
	authIndex := xaiAuth.EnsureIndex()

	h := &Handler{cfg: &config.Config{}, authManager: manager, tokenStore: store}
	router := gin.New()
	router.POST("/", h.APICall)

	const concurrency = 8
	var wg sync.WaitGroup
	errCh := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"authIndex":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `/billing","header":{"Authorization":"Bearer $TOKEN$"}}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				errCh <- fmt.Errorf("unexpected status %d: %s", rec.Code, rec.Body.String())
				return
			}
			var resp apiCallResponse
			if errDecode := json.Unmarshal(rec.Body.Bytes(), &resp); errDecode != nil {
				errCh <- fmt.Errorf("decode response: %w", errDecode)
				return
			}
			if resp.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("unexpected upstream status %d: body=%s", resp.StatusCode, resp.Body)
				return
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}
	if count := refreshCount.Load(); count == 0 {
		t.Fatal("expected at least one refresh call")
	}
}

func TestAPICallReplacesXAIOAuthAccessTokenInData(t *testing.T) {
	t.Parallel()

	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	xaiAuth := &coreauth.Auth{
		ID:       "xai-body-data@example.com.json",
		FileName: "xai-body-data@example.com.json",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.x.ai/v1",
		},
		Metadata: map[string]any{
			"type":          "xai",
			"auth_kind":     "oauth",
			"access_token":  "xai-body-data-token",
			"refresh_token": "xai-refresh-token",
			"base_url":      "https://api.x.ai/v1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
		t.Fatalf("register xai auth: %v", errRegister)
	}
	authIndex := xaiAuth.EnsureIndex()

	h := &Handler{cfg: &config.Config{}, authManager: manager}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"authIndex":"` + authIndex + `","method":"POST","url":"` + upstream.URL + `/probe","data":"{\"token\":\"$TOKEN$\",\"client\":\"grok\"}"}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	wantBody := `{"token":"xai-body-data-token","client":"grok"}`
	if receivedBody != wantBody {
		t.Fatalf("receivedBody = %q, want %q", receivedBody, wantBody)
	}
}

func TestAPICallRefreshesWhenOnlyIDTokenPresentWithRefreshToken(t *testing.T) {
	t.Parallel()

	var refreshCalls atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "xai-from-id-refresh",
			"refresh_token": "xai-new-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	xaiAuth := &coreauth.Auth{
		ID:       "xai-id-only@example.com.json",
		FileName: "xai-id-only@example.com.json",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.x.ai/v1",
		},
		Metadata: map[string]any{
			"type":           "xai",
			"auth_kind":      "oauth",
			"id_token":       "xai-raw-id-token",
			"refresh_token":  "xai-refresh-token",
			"token_endpoint": tokenServer.URL,
			"base_url":       "https://api.x.ai/v1",
		},
	}
	if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
		t.Fatalf("register xai auth: %v", errRegister)
	}
	authIndex := xaiAuth.EnsureIndex()

	h := &Handler{cfg: &config.Config{}, authManager: manager, tokenStore: store}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"authIndex":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `/billing","header":{"Authorization":"Bearer $TOKEN$"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if receivedAuth != "Bearer xai-from-id-refresh" {
		t.Fatalf("Authorization = %q, want 'Bearer xai-from-id-refresh'", receivedAuth)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refreshCalls = %d, want 1", refreshCalls.Load())
	}
}

func TestAPICallRejectsWhenOnlyIDTokenPresentWithoutRefreshToken(t *testing.T) {
	t.Parallel()

	var upstreamHit atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	xaiAuth := &coreauth.Auth{
		ID:       "xai-id-no-refresh@example.com.json",
		FileName: "xai-id-no-refresh@example.com.json",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.x.ai/v1",
		},
		Metadata: map[string]any{
			"type":      "xai",
			"auth_kind": "oauth",
			"id_token":  "xai-raw-id-token",
			"base_url":  "https://api.x.ai/v1",
		},
	}
	if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
		t.Fatalf("register xai auth: %v", errRegister)
	}
	authIndex := xaiAuth.EnsureIndex()

	h := &Handler{cfg: &config.Config{}, authManager: manager}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"authIndex":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `/billing","header":{"Authorization":"Bearer $TOKEN$"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	var respBody map[string]any
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &respBody); errDecode != nil {
		t.Fatalf("decode error body: %v", errDecode)
	}
	if got, _ := respBody["error"].(string); got != "auth token not found" {
		t.Fatalf("error = %q, want 'auth token not found'", got)
	}
	if upstreamHit.Load() {
		t.Fatal("upstream must not be hit when access token cannot be resolved")
	}
}

package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestKiroExecutorDoesNotLogTokenRefreshFailuresAtErrorLevel(t *testing.T) {
	source, err := os.ReadFile("kiro_executor.go")
	if err != nil {
		t.Fatalf("read kiro_executor.go: %v", err)
	}

	forbidden := `log.Errorf("kiro: token refresh failed:`
	if strings.Contains(string(source), forbidden) {
		t.Fatalf("kiro token refresh failures should be returned without duplicate error logs")
	}
}

func TestGetKiroEndpointConfigs_NilAuth(t *testing.T) {
	configs := getKiroEndpointConfigs(nil)

	// runtime, codewhisperer, amazonq in declared order (see
	// buildKiroEndpointConfigsForAuth / kiro_endpoint_rotation.go).
	if len(configs) != 3 {
		t.Fatalf("expected 3 endpoint configs, got %d", len(configs))
	}

	if configs[0].Name != "KiroRuntime" {
		t.Errorf("first config Name = %q, want %q", configs[0].Name, "KiroRuntime")
	}
	expectedURL := "https://runtime.us-east-1.kiro.dev/"
	if configs[0].URL != expectedURL {
		t.Errorf("first config URL = %q, want %q", configs[0].URL, expectedURL)
	}
}

func TestGetKiroEndpointConfigs_WithRegionFromProfileArn(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"profile_arn": "arn:aws:codewhisperer:ap-southeast-1:123456789012:profile/ABC",
		},
	}

	configs := getKiroEndpointConfigs(auth)

	if len(configs) != 3 {
		t.Fatalf("expected 3 endpoint configs, got %d", len(configs))
	}

	expectedURL := "https://runtime.ap-southeast-1.kiro.dev/"
	if configs[0].URL != expectedURL {
		t.Errorf("primary URL = %q, want %q", configs[0].URL, expectedURL)
	}
}

func TestBuildKiroEndpointConfigsForAuthSelectsHostByCredentialKind(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		wantName string
		wantURL  string
	}{
		{
			name: "builder id without profile uses Amazon Q",
			metadata: map[string]any{
				"auth_method":   "builder-id",
				"client_id":     "client",
				"client_secret": "secret",
				"api_region":    "eu-west-1",
			},
			wantName: "AmazonQ",
			wantURL:  "https://q.eu-west-1.amazonaws.com/generateAssistantResponse",
		},
		{
			name: "profiled OIDC uses Kiro Runtime",
			metadata: map[string]any{
				"auth_method":   "builder-id",
				"client_id":     "client",
				"client_secret": "secret",
				"profile_arn":   "arn:aws:codewhisperer:ap-southeast-1:123:profile/abc",
			},
			wantName: "KiroRuntime",
			wantURL:  "https://runtime.ap-southeast-1.kiro.dev/",
		},
		{
			name: "social desktop uses Kiro Runtime",
			metadata: map[string]any{
				"auth_method": "social",
				"api_region":  "us-west-2",
			},
			wantName: "KiroRuntime",
			wantURL:  "https://runtime.us-west-2.kiro.dev/",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configs := buildKiroEndpointConfigsForAuth(&cliproxyauth.Auth{Metadata: test.metadata})
			if len(configs) != 3 {
				t.Fatalf("endpoint count = %d, want 3", len(configs))
			}
			if configs[0].Name != test.wantName || configs[0].URL != test.wantURL {
				t.Fatalf("primary endpoint = %s %s, want %s %s", configs[0].Name, configs[0].URL, test.wantName, test.wantURL)
			}
		})
	}
}

func TestGetKiroEndpointConfigs_WithApiRegionOverride(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"api_region":  "eu-central-1",
			"profile_arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
		},
	}

	configs := getKiroEndpointConfigs(auth)

	// api_region should take precedence over profile_arn
	expectedURL := "https://runtime.eu-central-1.kiro.dev/"
	if configs[0].URL != expectedURL {
		t.Errorf("primary URL = %q, want %q", configs[0].URL, expectedURL)
	}
}

func TestGetKiroEndpointConfigs_PreferredEndpoint(t *testing.T) {
	tests := []struct {
		name              string
		preference        string
		expectedFirstName string
	}{
		{
			name:              "Prefer codewhisperer",
			preference:        "codewhisperer",
			expectedFirstName: "KiroRuntime",
		},
		{
			name:              "Prefer ide (alias for codewhisperer)",
			preference:        "ide",
			expectedFirstName: "KiroRuntime",
		},
		{
			name:              "Prefer amazonq",
			preference:        "amazonq",
			expectedFirstName: "KiroRuntime",
		},
		{
			name:              "Prefer q (alias for amazonq)",
			preference:        "q",
			expectedFirstName: "KiroRuntime",
		},
		{
			name:              "Prefer cli (alias for amazonq)",
			preference:        "cli",
			expectedFirstName: "KiroRuntime",
		},
		{
			name:              "Unknown preference - no reordering",
			preference:        "unknown",
			expectedFirstName: "KiroRuntime",
		},
		{
			name:              "Empty preference - no reordering",
			preference:        "",
			expectedFirstName: "KiroRuntime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{
				Metadata: map[string]any{
					"preferred_endpoint": tt.preference,
				},
			}

			configs := getKiroEndpointConfigs(auth)

			if configs[0].Name != tt.expectedFirstName {
				t.Errorf("first endpoint Name = %q, want %q", configs[0].Name, tt.expectedFirstName)
			}
		})
	}
}

func TestGetKiroEndpointConfigs_PreferredEndpointFromAttributes(t *testing.T) {
	// Test that preferred_endpoint can also come from Attributes
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{},
		Attributes: map[string]string{"preferred_endpoint": "codewhisperer"},
	}

	configs := getKiroEndpointConfigs(auth)

	if configs[0].Name != "KiroRuntime" {
		t.Errorf("first endpoint Name = %q, want %q", configs[0].Name, "KiroRuntime")
	}
}

func TestGetKiroEndpointConfigs_MetadataTakesPrecedenceOverAttributes(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{"preferred_endpoint": "amazonq"},
		Attributes: map[string]string{"preferred_endpoint": "codewhisperer"},
	}

	configs := getKiroEndpointConfigs(auth)

	if configs[0].Name != "KiroRuntime" {
		t.Errorf("first endpoint Name = %q, want %q", configs[0].Name, "KiroRuntime")
	}
}

func TestGetAuthValue(t *testing.T) {
	tests := []struct {
		name     string
		auth     *cliproxyauth.Auth
		key      string
		expected string
	}{
		{
			name: "From metadata",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{"test_key": "metadata_value"},
			},
			key:      "test_key",
			expected: "metadata_value",
		},
		{
			name: "From attributes (fallback)",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "attribute_value",
		},
		{
			name: "Metadata takes precedence",
			auth: &cliproxyauth.Auth{
				Metadata:   map[string]any{"test_key": "metadata_value"},
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "metadata_value",
		},
		{
			name: "Key not found",
			auth: &cliproxyauth.Auth{
				Metadata:   map[string]any{"other_key": "value"},
				Attributes: map[string]string{"another_key": "value"},
			},
			key:      "test_key",
			expected: "",
		},
		{
			name: "Nil metadata",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "attribute_value",
		},
		{
			name:     "Both nil",
			auth:     &cliproxyauth.Auth{},
			key:      "test_key",
			expected: "",
		},
		{
			name: "Value is trimmed and lowercased",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{"test_key": "  UPPER_VALUE  "},
			},
			key:      "test_key",
			expected: "upper_value",
		},
		{
			name: "Empty string value in metadata - falls back to attributes",
			auth: &cliproxyauth.Auth{
				Metadata:   map[string]any{"test_key": ""},
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "attribute_value",
		},
		{
			name: "Non-string value in metadata - falls back to attributes",
			auth: &cliproxyauth.Auth{
				Metadata:   map[string]any{"test_key": 123},
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "attribute_value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getAuthValue(tt.auth, tt.key)
			if result != tt.expected {
				t.Errorf("getAuthValue() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestKiroRefreshReloadsRawFileOnceAndRetainsRefreshToken(t *testing.T) {
	var refreshTokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode refresh request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		refreshToken, _ := request["refreshToken"].(string)
		refreshTokens = append(refreshTokens, refreshToken)
		if refreshToken == "stale-refresh" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"new-access","expiresIn":3600}`))
	}))
	defer server.Close()

	authPath := filepath.Join(t.TempDir(), "kiro.json")
	if err := os.WriteFile(authPath, []byte(`{
		"refreshToken":"fresh-refresh",
		"clientId":"client",
		"clientSecret":"secret",
		"authMethod":"builder-id"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	executor := NewKiroExecutor(&config.Config{
		OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{
			"kiro": {ApiBaseURL: server.URL},
		},
	})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"path": authPath},
		Metadata: map[string]any{
			"refresh_token": "stale-refresh",
			"client_id":     "client",
			"client_secret": "secret",
			"auth_method":   "builder-id",
		},
	}

	updated, err := executor.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if len(refreshTokens) != 2 || refreshTokens[0] != "stale-refresh" || refreshTokens[1] != "fresh-refresh" {
		t.Fatalf("refresh attempts = %#v, want [stale-refresh fresh-refresh]", refreshTokens)
	}
	if got := updated.Metadata["access_token"]; got != "new-access" {
		t.Fatalf("access_token = %#v, want new-access", got)
	}
	if got := updated.Metadata["refresh_token"]; got != "fresh-refresh" {
		t.Fatalf("refresh_token = %#v, want retained fresh-refresh", got)
	}
}

func TestGetAccountKey(t *testing.T) {
	tests := []struct {
		name    string
		auth    *cliproxyauth.Auth
		checkFn func(t *testing.T, result string)
	}{
		{
			name: "From client_id",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{
					"client_id":     "test-client-id-123",
					"refresh_token": "test-refresh-token-456",
				},
			},
			checkFn: func(t *testing.T, result string) {
				expected := kiroauth.GetAccountKey("test-client-id-123", "test-refresh-token-456")
				if result != expected {
					t.Errorf("expected %s, got %s", expected, result)
				}
			},
		},
		{
			name: "From refresh_token only",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{
					"refresh_token": "test-refresh-token-789",
				},
			},
			checkFn: func(t *testing.T, result string) {
				expected := kiroauth.GetAccountKey("", "test-refresh-token-789")
				if result != expected {
					t.Errorf("expected %s, got %s", expected, result)
				}
			},
		},
		{
			name: "Nil auth",
			auth: nil,
			checkFn: func(t *testing.T, result string) {
				if len(result) != 16 {
					t.Errorf("expected 16 char key, got %d chars", len(result))
				}
			},
		},
		{
			name: "Nil metadata",
			auth: &cliproxyauth.Auth{},
			checkFn: func(t *testing.T, result string) {
				if len(result) != 16 {
					t.Errorf("expected 16 char key, got %d chars", len(result))
				}
			},
		},
		{
			name: "Empty metadata",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{},
			},
			checkFn: func(t *testing.T, result string) {
				if len(result) != 16 {
					t.Errorf("expected 16 char key, got %d chars", len(result))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getAccountKey(tt.auth)
			tt.checkFn(t, result)
		})
	}
}

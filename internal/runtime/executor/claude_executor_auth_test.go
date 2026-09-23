package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestClaudeExecutorOAuthUsesStandardHTTPTransport(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	oauth := &cliproxyauth.Auth{Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}
	client := executor.httpClient(t.Context(), oauth, "sk-ant-oat-access")
	if _, ok := helps.UnwrapStreamingIdleTimeout(client.Transport).(*http.Transport); !ok {
		t.Fatalf("OAuth transport = %T, want standard *http.Transport", client.Transport)
	}
	apiKey := &cliproxyauth.Auth{Provider: "claude", Attributes: map[string]string{"api_key": "sk-ant-api-access"}}
	client = executor.httpClient(t.Context(), apiKey, "sk-ant-api-access")
	if _, ok := helps.UnwrapStreamingIdleTimeout(client.Transport).(*http.Transport); ok {
		t.Fatal("API-key transport switched to OAuth standard transport")
	}
}

func TestClaudeExecutorOAuthMessagesReachLocalUpstream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "non-stream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			received := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- r.Header.Get("Authorization")
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()

			auth := &cliproxyauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "sk-ant-oat-local-upstream", "auth_kind": "oauth", "base_url": server.URL},
				Metadata:   claudeOAuthTestMetadata(),
			}
			payload := []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
			options := cliproxyexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatClaude}
			executor := NewClaudeExecutor(&config.Config{})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if stream {
				result, err := executor.ExecuteStream(ctx, auth, request, options)
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			} else if _, err := executor.Execute(ctx, auth, request, options); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			select {
			case got := <-received:
				if got != "Bearer sk-ant-oat-local-upstream" {
					t.Fatalf("upstream Authorization = %q, want OAuth bearer", got)
				}
			case <-ctx.Done():
				t.Fatal("OAuth upstream request was not observed before deadline")
			}
		})
	}
}

func TestClaudeExecutorDuplicateMetadataReachesUpstream(t *testing.T) {
	testCases := []struct {
		name string
		run  func(context.Context, *ClaudeExecutor, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			run: func(ctx context.Context, executor *ClaudeExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errExecute := executor.Execute(ctx, auth, req, opts)
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(ctx context.Context, executor *ClaudeExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errStream := executor.ExecuteStream(ctx, auth, req, opts)
				return errStream
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			upstreamCalled := false
			errUpstream := errors.New("upstream reached")
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamCalled = true
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					t.Errorf("read upstream request: %v", errRead)
				}
				if got := bytes.Count(body, []byte(`"metadata":`)); got != 1 {
					t.Errorf("upstream metadata members = %d, want one after JSON normalization", got)
				}
				return nil, errUpstream
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "sk-ant-oat-duplicate-metadata", "auth_kind": "oauth"},
				Metadata: map[string]any{
					"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
					claudeauth.ClaudeDeviceIDsMetadataKey: []string{
						"0000000000000000000000000000000000000000000000000000000000000000",
					},
				},
			}
			req := cliproxyexecutor.Request{
				Model: "claude-opus-5",
				Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}],` +
					`"metadata":{"user_id":"{}"},"metadata":{"user_id":"{}"}}`),
			}
			errRun := testCase.run(ctx, NewClaudeExecutor(&config.Config{}), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if !upstreamCalled {
				t.Fatal("duplicate metadata did not reach upstream")
			}
			if !errors.Is(errRun, errUpstream) {
				t.Fatalf("error = %v, want upstream error", errRun)
			}
		})
	}
}

func TestClaudeExecutorPrepareRequestAuthPopulatesCredentialIdentity(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(_ context.Context, _ *cliproxyauth.Auth, accessToken string) (*claudeauth.OAuthProfile, error) {
		if accessToken != "sk-ant-oat-prepare" {
			t.Fatalf("access token = %q, want selected credential token", accessToken)
		}
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		profile.Account.Email = "user@example.com"
		profile.Organization.UUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		profile.Organization.Name = "Example Org"
		return profile, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-old-credential",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat-prepare",
		},
		Metadata: map[string]any{"type": "claude"},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing credential identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	deviceIDs := claudeauth.NormalizeDeviceIDPool(prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	if len(deviceIDs) != claudeauth.ClaudeDevicePoolSize {
		t.Fatalf("device pool length = %d, want %d", len(deviceIDs), claudeauth.ClaudeDevicePoolSize)
	}
	if got := prepared.Metadata["account_uuid"]; got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account_uuid = %#v, want upstream profile account", got)
	}
	if got := prepared.Metadata["organization_uuid"]; got != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("organization_uuid = %#v, want upstream profile organization", got)
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuthMigratesFiveDevicesToOne(t *testing.T) {
	legacy := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444444444444444444444444444",
	}
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		t.Fatal("profile lookup should not run when account UUID is already present")
		return nil, nil
	}
	auth := &cliproxyauth.Auth{
		ID:         "claude-five-device-credential",
		Attributes: map[string]string{"api_key": "sk-ant-oat-five-device"},
		Metadata: map[string]any{
			"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			claudeauth.ClaudeDeviceIDsMetadataKey: legacy,
		},
	}
	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for legacy five-device pool")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	deviceIDs, ok := prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey].([]string)
	if !ok || len(deviceIDs) != 1 || deviceIDs[0] != legacy[0] {
		t.Fatalf("prepared device IDs = %#v, want first legacy device only", prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after single-device migration")
	}
}

func TestClaudeExecutorPrepareRequestAuthIgnoresFreshTimestampWithoutIdentity(t *testing.T) {
	calls := 0
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		calls++
		return nil, fmt.Errorf("profile unavailable")
	}
	const previousCheckedAt = "2999-01-01T00:00:00Z"
	auth := &cliproxyauth.Auth{
		ID:         "claude-profile-unavailable",
		Attributes: map[string]string{"api_key": "sk-ant-oat-profile-unavailable"},
		Metadata: map[string]any{
			"type":                                "claude",
			claudeAccountProfileCheckedAtKey:      previousCheckedAt,
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{"0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}

	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare == nil {
		t.Fatal("PrepareRequestAuth() error = nil, want missing account identity failure")
	}
	if prepared != nil {
		t.Fatalf("PrepareRequestAuth() auth = %#v, want nil on missing account identity", prepared)
	}
	if calls != 1 {
		t.Fatalf("profile calls = %d, want 1", calls)
	}
	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false after failed profile lookup; failure must remain retryable")
	}
	if got := claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileCheckedAtKey); got != previousCheckedAt {
		t.Fatalf("profile checked timestamp = %q, want prior value preserved without suppressing retry", got)
	}
}

func TestClaudeExecutorPrepareRequestAuthSetupTokenBypassesProfile(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		t.Fatal("profile fetcher should NOT be called for setup-tokens")
		return nil, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-setuptoken.json",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat01-test-setup-token-value",
		},
		Metadata: map[string]any{
			"type":   "claude",
			"scopes": "user:inference user:ccr_inference user:file_upload",
		},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing setup-token identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	if prepared == nil {
		t.Fatal("prepared auth is nil")
	}
	accountUUID := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid")
	if accountUUID == "" {
		t.Fatal("account_uuid is empty after setup-token preparation")
	}
	deviceIDs := claudeauth.NormalizeDeviceIDPool(prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	if len(deviceIDs) != 1 {
		t.Fatalf("device pool length = %d, want 1", len(deviceIDs))
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after setup-token identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuth403ScopeFallback(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	fetchCalls := 0
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		fetchCalls++
		return nil, fmt.Errorf("fetch Claude OAuth profile failed with status 403: permission_error: OAuth token does not meet scope requirement any_of(user:profile, user:office)")
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-scope-restricted-credential",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat01-scope-restricted",
		},
		Metadata: map[string]any{
			"type":          "claude",
			"refresh_token": "dummy-refresh-token",
		},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() with 403 error = %v, want fallback success", errPrepare)
	}
	if prepared == nil {
		t.Fatal("prepared auth is nil")
	}
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls = %d, want 1", fetchCalls)
	}
	accountUUID := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid")
	if accountUUID == "" {
		t.Fatal("account_uuid is empty after 403 fallback")
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after 403 identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuthSkipAccountProfileConfig(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		t.Fatal("profile fetcher should NOT be called when skip_account_profile is true")
		return nil, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-skip-profile.json",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat01-skip-profile",
		},
		Metadata: map[string]any{
			"type":                 "claude",
			"skip_account_profile": true,
		},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	if prepared == nil {
		t.Fatal("prepared auth is nil")
	}
	accountUUID := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid")
	if accountUUID == "" {
		t.Fatal("account_uuid is empty after skip_account_profile preparation")
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuthEmptyAccountUUIDInProfileFallback(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	fetchCalls := 0
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		fetchCalls++
		return &claudeauth.OAuthProfile{}, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-empty-uuid-in-profile",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat01-empty-uuid",
		},
		Metadata: map[string]any{
			"type": "claude",
		},
	}

	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v, want fallback on empty UUID", errPrepare)
	}
	if prepared == nil {
		t.Fatal("prepared auth is nil")
	}
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls = %d, want 1", fetchCalls)
	}
	accountUUID := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid")
	if accountUUID == "" {
		t.Fatal("account_uuid is empty after fallback")
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after identity was populated")
	}
}

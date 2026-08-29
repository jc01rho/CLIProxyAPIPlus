package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestZcodeCredsFallsBackToMetadataAccessToken pins the on-disk auth-file shape.
//
// zcode auth files persist as the flat TokenStorage form (type/access_token/
// refresh_token/email/account_id/expires_at) and carry no "attributes" object,
// so a record reloaded from disk after a restart or config reload has a nil
// Attributes map. Credentials.AccessToken is documented in
// internal/auth/zcode/zcode.go as the provisioned Z.AI API key "{id}.{secret}",
// which makes Metadata["access_token"] the same value Attributes["api_key"]
// holds right after OAuth. Without this fallback the executor silently loses the
// key on every reload: model discovery falls back to the static catalog and
// inference requests go out unauthenticated.
func TestZcodeCredsFallsBackToMetadataAccessToken(t *testing.T) {
	const provisionedKey = "1234567890abcdef.SecretPortionOfTheKey"

	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "attributes api_key wins when present",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"api_key": provisionedKey},
				Metadata:   map[string]any{"access_token": "stale-value"},
			},
			want: provisionedKey,
		},
		{
			name: "reloaded auth file with no attributes falls back to metadata",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{
					"type":         "zcode",
					"access_token": provisionedKey,
				},
			},
			want: provisionedKey,
		},
		{
			name: "empty attributes api_key falls back to metadata",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"api_key": "   "},
				Metadata:   map[string]any{"access_token": provisionedKey},
			},
			want: provisionedKey,
		},
		{
			name: "metadata access_token is trimmed",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{"access_token": "  " + provisionedKey + "  "},
			},
			want: provisionedKey,
		},
		{
			name: "no credential anywhere yields empty",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{"type": "zcode"},
			},
			want: "",
		},
		{
			name: "non-string metadata access_token yields empty",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{"access_token": 12345},
			},
			want: "",
		},
		{
			name: "nil auth yields empty",
			auth: nil,
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := zcodeCreds(tc.auth); got != tc.want {
				t.Fatalf("zcodeCreds() = %q, want %q", got, tc.want)
			}
		})
	}
}

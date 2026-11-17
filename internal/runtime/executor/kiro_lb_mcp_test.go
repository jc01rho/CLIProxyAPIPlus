package executor

import (
	"context"
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Ported from kiro-lb src/web_search.rs (807c8ab): the MCP call opts in to
// sending the query upstream and announces the credential's profile ARN.
func TestWebSearchMCPHeadersMatchKiroLB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		auth     *cliproxyauth.Auth
		wantARN  string
		wantNone bool
	}{
		{
			name:    "builder id falls back to the shared ARN",
			auth:    &cliproxyauth.Auth{ID: "builder", Metadata: map[string]any{"access_token": "tok", "auth_method": "builder-id"}},
			wantARN: kiroBuilderIDProfileARN,
		},
		{
			name:    "explicit profile ARN wins",
			auth:    &cliproxyauth.Auth{ID: "social", Metadata: map[string]any{"access_token": "tok", "auth_method": "social", "profile_arn": "arn:aws:codewhisperer:us-east-1:1:profile/EXPLICIT"}},
			wantARN: "arn:aws:codewhisperer:us-east-1:1:profile/EXPLICIT",
		},
		{
			name:     "credential without a profile ARN sends none",
			auth:     &cliproxyauth.Auth{ID: "social-2", Metadata: map[string]any{"access_token": "tok", "auth_method": "social"}},
			wantNone: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			handler := newWebSearchHandler(context.Background(), "https://q.us-east-1.amazonaws.com/mcp", "tok", nil, test.auth, nil)
			req, err := http.NewRequest(http.MethodPost, "https://q.us-east-1.amazonaws.com/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			handler.setMcpHeaders(req)

			if got := req.Header.Get("x-amzn-codewhisperer-optout"); got != "false" {
				t.Fatalf("x-amzn-codewhisperer-optout = %q, want %q", got, "false")
			}
			gotARN := req.Header.Get("x-amzn-kiro-profile-arn")
			if test.wantNone {
				if gotARN != "" {
					t.Fatalf("x-amzn-kiro-profile-arn = %q, want empty", gotARN)
				}
				return
			}
			if gotARN != test.wantARN {
				t.Fatalf("x-amzn-kiro-profile-arn = %q, want %q", gotARN, test.wantARN)
			}
		})
	}
}

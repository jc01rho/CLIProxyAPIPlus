package executor

import (
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestApplyCodexReferenceBodyDoc(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		responsesLite bool
		want          string
	}{
		{
			name: "function tool keeps reference key order, strict defaults false, schema dropped",
			body: `{"model":"gpt-6-sol","tools":[{"parameters":{"$schema":"http://json-schema.org/draft-07/schema#","type":"object"},"x_extra":1,"name":"read","type":"function","description":"d"}]}`,
			want: `{"model":"gpt-6-sol","tools":[{"type":"function","name":"read","description":"d","strict":false,"parameters":{"type":"object"},"x_extra":1}],"parallel_tool_calls":true,"client_metadata":{"x-codex-installation-id":"inst","x-codex-window-id":"win"}}`,
		},
		{
			name: "explicit strict and non-function tools are untouched",
			body: `{"tools":[{"type":"function","name":"a","strict":true,"parameters":{"type":"object"}},{"type":"web_search"}],"parallel_tool_calls":false}`,
			want: `{"tools":[{"type":"function","name":"a","strict":true,"parameters":{"type":"object"}},{"type":"web_search"}],"parallel_tool_calls":false,"client_metadata":{"x-codex-installation-id":"inst","x-codex-window-id":"win"}}`,
		},
		{
			name: "caller client_metadata keys stay first",
			body: `{"client_metadata":{"caller":"x"}}`,
			want: `{"client_metadata":{"caller":"x","x-codex-installation-id":"inst","x-codex-window-id":"win"},"parallel_tool_calls":true}`,
		},
		{
			name:          "responses lite keeps its own parallel_tool_calls",
			body:          `{"model":"gpt-6-sol"}`,
			responsesLite: true,
			want:          `{"model":"gpt-6-sol","client_metadata":{"x-codex-installation-id":"inst","x-codex-window-id":"win"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := ojson.NewDoc([]byte(tt.body))
			applyCodexReferenceBodyDoc(doc, "inst", "win", tt.responsesLite)
			if got := string(doc.Bytes()); got != tt.want {
				t.Fatalf("body\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestCodexUsesReferenceBodyGates(t *testing.T) {
	oauth := &cliproxyauth.Auth{ID: "a", Metadata: map[string]any{"access_token": "tok"}}
	apiKey := &cliproxyauth.Auth{ID: "b", Attributes: map[string]string{"api_key": "key"}}
	const chatgpt = "https://chatgpt.com/backend-api/codex/responses"
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		url  string
		want bool
	}{
		{"oauth on chatgpt backend", oauth, chatgpt, true},
		{"api key credential", apiKey, chatgpt, false},
		{"nil auth", nil, chatgpt, false},
		{"other host", oauth, "https://example.com/backend-api/codex/responses", false},
		{"other path", oauth, "https://chatgpt.com/backend-api/other/responses", false},
		{"compact endpoint", oauth, chatgpt + "/compact", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexUsesReferenceBody(t.Context(), tt.auth, http.Header{}, tt.url); got != tt.want {
				t.Fatalf("codexUsesReferenceBody() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCodexInstallationIDIsStablePerCredential(t *testing.T) {
	if codexInstallationID("") != "" {
		t.Fatal("empty credential must not get an installation id")
	}
	first, second := codexInstallationID("auth-1"), codexInstallationID("auth-1")
	if first == "" || first != second {
		t.Fatalf("installation id not stable: %q vs %q", first, second)
	}
	if first == codexInstallationID("auth-2") {
		t.Fatal("different credentials must not share an installation id")
	}
}

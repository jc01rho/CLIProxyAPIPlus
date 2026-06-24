package executor

import (
	"encoding/json"
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func Test_BuildCommandCodePayload_serializes_message_content_as_strings(t *testing.T) {
	// Given
	payload := []byte(`{
		"model": "parrot",
		"messages": [
			{"role": "system", "content": "system instructions"},
			{"role": "user", "content": [
				{"type": "text", "text": "hello"},
				{"type": "text", "text": "world"},
				{"type": "image_url", "image_url": {"url": "https://example.test/image.png"}}
			]},
			{"role": "assistant", "content": "plain answer"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "lookup", "arguments": "{\"query\":\"sparrow\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "tool output"}
		]
	}`)

	// When
	got, err := buildCommandCodePayload(payload, "nvidia/nemotron-3-ultra-550b-a55b", false)

	// Then
	if err != nil {
		t.Fatalf("buildCommandCodePayload() error = %v", err)
	}
	if got := gjson.GetBytes(got, "params.system").String(); got != "system instructions" {
		t.Fatalf("params.system = %q, want %q", got, "system instructions")
	}

	messages := gjson.GetBytes(got, "params.messages").Array()
	if len(messages) != 3 {
		t.Fatalf("len(params.messages) = %d, want 3", len(messages))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want %q", got, "user")
	}
	assertCommandCodeMessageContent(t, messages[0], "hello\nworld")
	assertCommandCodeMessageContent(t, messages[1], "plain answer")
	assertCommandCodeMessageContent(t, messages[2], "tool call lookup {\"query\":\"sparrow\"}")

	var envelope struct {
		Params struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		} `json:"params"`
	}
	if err := json.Unmarshal(got, &envelope); err != nil {
		t.Fatalf("unmarshal envelope with string message content: %v", err)
	}
}

func Test_ApplyCommandCodeHeaders_matches_provider_cli_auth_headers(t *testing.T) {
	// Given
	req, err := http.NewRequest(http.MethodPost, "https://api.commandcode.ai/alpha/generate", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	// When
	applyCommandCodeHeaders(req, "user_test")

	// Then
	if got := req.Header.Get("Authorization"); got != "Bearer user_test" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer user_test")
	}
	if got := req.Header.Get("x-command-code-version"); got != "0.29.0" {
		t.Fatalf("x-command-code-version = %q, want %q", got, "0.29.0")
	}
	if got := req.Header.Get("x-cli-environment"); got != "production" {
		t.Fatalf("x-cli-environment = %q, want %q", got, "production")
	}
	if got := req.Header.Get("x-project-slug"); got != "cli-proxy" {
		t.Fatalf("x-project-slug = %q, want %q", got, "cli-proxy")
	}
	if got := req.Header.Get("x-taste-learning"); got != "true" {
		t.Fatalf("x-taste-learning = %q, want %q", got, "true")
	}
	if got := req.Header.Get("x-co-flag"); got != "false" {
		t.Fatalf("x-co-flag = %q, want %q", got, "false")
	}
	if got := req.Header.Get("x-session-id"); got != "" {
		t.Fatalf("x-session-id = %q, want empty header", got)
	}
}

func Test_CommandCodeGenerateURL_uses_default_and_configured_base_url(t *testing.T) {
	// Given
	defaultAuth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "user_default"}}
	customAuth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "user_custom",
		"base_url": "https://mock.commandcode.test/",
	}}

	if got := commandCodeGenerateURL(defaultAuth); got != "https://api.commandcode.ai/alpha/generate" {
		t.Fatalf("default generate URL = %q", got)
	}
	if got := commandCodeGenerateURL(customAuth); got != "https://mock.commandcode.test/alpha/generate" {
		t.Fatalf("custom generate URL = %q", got)
	}
}

func Test_CommandCodeAPIKey_accepts_provider_auth_field_aliases(t *testing.T) {
	tests := []struct {
		name       string
		attrs      map[string]string
		wantAPIKey string
	}{
		{
			name:       "config api_key",
			attrs:      map[string]string{"api_key": " user_config "},
			wantAPIKey: "user_config",
		},
		{
			name:       "commandcode auth apiKey",
			attrs:      map[string]string{"apiKey": " user_file "},
			wantAPIKey: "user_file",
		},
		{
			name:       "custom key",
			attrs:      map[string]string{"key": " user_custom "},
			wantAPIKey: "user_custom",
		},
		{
			name:       "legacy commandcode field",
			attrs:      map[string]string{"commandcode": " user_legacy "},
			wantAPIKey: "user_legacy",
		},
		{
			name:       "oauth access field",
			attrs:      map[string]string{"access": " user_oauth_access "},
			wantAPIKey: "user_oauth_access",
		},
		{
			name: "prefers config api_key",
			attrs: map[string]string{
				"api_key":     " user_primary ",
				"commandcode": "user_secondary",
			},
			wantAPIKey: "user_primary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commandCodeAPIKey(&cliproxyauth.Auth{Attributes: tt.attrs})
			if got != tt.wantAPIKey {
				t.Fatalf("commandCodeAPIKey() = %q, want %q", got, tt.wantAPIKey)
			}
		})
	}
}

func assertCommandCodeMessageContent(t *testing.T, message gjson.Result, want string) {
	t.Helper()
	if got := message.Get("content").Type; got != gjson.String {
		t.Fatalf("message content type = %v, want %v; raw=%s", got, gjson.String, message.Raw)
	}
	if got := message.Get("content").String(); got != want {
		t.Fatalf("message content = %q, want %q", got, want)
	}
}

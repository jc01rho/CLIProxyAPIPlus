package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func Test_BuildCommandCodePayload_serializes_typed_message_content_blocks(t *testing.T) {
	// Given a payload that mixes system instructions, multimodal user content,
	// a plain assistant answer, an assistant tool call, and its tool result.
	payload := []byte(`{
		"model": "parrot",
		"messages": [
			{"role": "system", "content": "system instructions"},
			{"role": "user", "content": [
				{"type": "text", "text": "hello"},
				{"type": "text", "text": "world"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,aGVsbG8="}}
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

	// Then system content is extracted and every message survives with typed
	// content blocks matching the command-code@1.6.0 wire contract.
	if err != nil {
		t.Fatalf("buildCommandCodePayload() error = %v", err)
	}
	if got := gjson.GetBytes(got, "params.system").String(); got != "system instructions" {
		t.Fatalf("params.system = %q, want %q", got, "system instructions")
	}

	messages := gjson.GetBytes(got, "params.messages").Array()
	if len(messages) != 4 {
		t.Fatalf("len(params.messages) = %d, want 4 (tool result history must be preserved)", len(messages))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want %q", got, "user")
	}
	userTexts := messages[0].Get("content")
	if !userTexts.IsArray() {
		t.Fatalf("user content is not a typed JSON array; raw=%s", userTexts.Raw)
	}
	if got := userTexts.Get("0.text").String(); got != "hello" {
		t.Fatalf("user content[0].text = %q, want %q", got, "hello")
	}
	if got := userTexts.Get("1.text").String(); got != "world" {
		t.Fatalf("user content[1].text = %q, want %q", got, "world")
	}
	if got := commandCodeBlockByType(t, messages[0], "image").Get("image").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("user image block data URL = %q, want %q", got, "data:image/png;base64,aGVsbG8=")
	}
	if got := commandCodeBlockByType(t, messages[1], "text").Get("text").String(); got != "plain answer" {
		t.Fatalf("assistant text block = %q, want %q", got, "plain answer")
	}
	toolCall := commandCodeBlockByType(t, messages[2], "tool-call")
	if got := toolCall.Get("toolCallId").String(); got != "call_1" {
		t.Fatalf("tool-call toolCallId = %q, want %q", got, "call_1")
	}
	if got := toolCall.Get("toolName").String(); got != "lookup" {
		t.Fatalf("tool-call toolName = %q, want %q", got, "lookup")
	}
	if got := toolCall.Get("input.query").String(); got != "sparrow" {
		t.Fatalf("tool-call input.query = %q, want %q", got, "sparrow")
	}
	toolResult := commandCodeBlockByType(t, messages[3], "tool-result")
	if got := toolResult.Get("toolCallId").String(); got != "call_1" {
		t.Fatalf("tool-result toolCallId = %q, want %q", got, "call_1")
	}
	if got := toolResult.Get("output.value").String(); got != "tool output" {
		t.Fatalf("tool-result output.value = %q, want %q", got, "tool output")
	}
}

func TestResolveCommandCodeModelNameForNestedAPIKey(t *testing.T) {
	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{
			{
				BaseURL: "https://commandcode.example/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "key-a"},
					{APIKey: "key-b", ProxyURL: "socks5://proxy-b.example:1080"},
				},
				Models: []config.CommandCodeModel{
					{Name: "upstream-model", Alias: "commandcode-visible"},
				},
			},
		},
	}
	auth := &cliproxyauth.Auth{
		ProxyURL: "socks5://proxy-b.example:1080",
		Attributes: map[string]string{
			cliproxyauth.AttributeAPIKey: "key-b",
		},
	}

	if got := resolveCommandCodeModelName(cfg, auth, "commandcode-visible"); got != "upstream-model" {
		t.Fatalf("resolved model = %q, want upstream-model", got)
	}
}

func Test_ApplyCommandCodeHeaders_matches_provider_cli_auth_headers(t *testing.T) {
	// Given
	req, err := http.NewRequest(http.MethodPost, "https://api.commandcode.ai/alpha/generate", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	// When
	applyCommandCodeHeaders(req, "user_test", "01890a5d-ac96-774b-bcce-b302099a8057")

	// Then
	if got := req.Header.Get("User-Agent"); got != "cli" {
		t.Fatalf("User-Agent = %q, want %q (upstream rejects proxy-fingerprinted requests)", got, "cli")
	}
	if got := req.Header.Get("Authorization"); got != "Bearer user_test" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer user_test")
	}
	if got := req.Header.Get("x-command-code-version"); got != "1.12.0" {
		t.Fatalf("x-command-code-version = %q, want %q", got, "1.12.0")
	}
	if got := req.Header.Get("x-cli-environment"); got != "production" {
		t.Fatalf("x-cli-environment = %q, want %q", got, "production")
	}
	if got := req.Header.Get("x-project-slug"); got != "workspace" {
		t.Fatalf("x-project-slug = %q, want %q", got, "workspace")
	}
	if got := req.Header.Get("x-taste-learning"); got != "false" {
		t.Fatalf("x-taste-learning = %q, want %q", got, "false")
	}
	if got := req.Header.Get("x-co-flag"); got != "false" {
		t.Fatalf("x-co-flag = %q, want %q", got, "false")
	}
	if got := req.Header.Get("x-session-id"); got != "01890a5d-ac96-774b-bcce-b302099a8057" {
		t.Fatalf("x-session-id = %q, want %q", got, "01890a5d-ac96-774b-bcce-b302099a8057")
	}
	if got := req.Header.Get("x-oauth-token"); got != "" {
		t.Fatalf("x-oauth-token = %q, want empty header", got)
	}
	if got := req.Header.Get("x-oauth-provider"); got != "" {
		t.Fatalf("x-oauth-provider = %q, want empty header", got)
	}
	if got := req.Header.Get("x-oss-primary-provider"); got != "" {
		t.Fatalf("x-oss-primary-provider = %q, want empty header", got)
	}
	if got := req.Header.Get("x-cmd-zdr"); got != "" {
		t.Fatalf("x-cmd-zdr = %q, want empty header", got)
	}
}

func Test_ApplyCommandCodeHeaders_omits_session_id_when_empty(t *testing.T) {
	// Given
	req, err := http.NewRequest(http.MethodPost, "https://api.commandcode.ai/alpha/generate", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	// When
	applyCommandCodeHeaders(req, "user_test", "")

	// Then
	if got := req.Header.Get("x-session-id"); got != "" {
		t.Fatalf("x-session-id = %q, want empty header when sessionID is empty", got)
	}
}

func Test_NewCommandCodeSessionID_matches_cli_uuid_format(t *testing.T) {
	// Given / When
	got := newCommandCodeSessionID()

	// Then: the CLI sends a UUID in x-session-id; any other shape is a
	// proxy fingerprint.
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("session ID %q is not a valid UUID: %v", got, err)
	}
}

func Test_NewCommandCodeSessionID_returns_unique_values(t *testing.T) {
	// Given / When
	seen := make(map[string]struct{})
	for i := 0; i < 64; i++ {
		seen[newCommandCodeSessionID()] = struct{}{}
	}

	// Then: 64 random IDs should be unique (collision probability is
	// negligible for 8-byte random IDs).
	if len(seen) != 64 {
		t.Fatalf("unique session IDs = %d, want 64", len(seen))
	}
}

func Test_CommandCodeGenerateURL_uses_default_and_configured_base_url(t *testing.T) {
	// Given
	defaultAuth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "user_default"}}
	customAuth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "user_custom",
		"base_url": "https://mock.commandcode.test/",
	}}
	catalogAuth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "user_catalog",
		"base_url": "https://mock.commandcode.test/provider/v1/models",
	}}

	if got := commandCodeGenerateURL(defaultAuth); got != "https://api.commandcode.ai/alpha/generate" {
		t.Fatalf("default generate URL = %q", got)
	}
	if got := commandCodeGenerateURL(customAuth); got != "https://mock.commandcode.test/alpha/generate" {
		t.Fatalf("custom generate URL = %q", got)
	}
	if got := commandCodeGenerateURL(catalogAuth); got != "https://mock.commandcode.test/alpha/generate" {
		t.Fatalf("catalog-path generate URL = %q", got)
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

func Test_CommandCodeFallbackText_recovers_unrecognized_ndjson_events(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "plain text", line: `{"type":"unknown","text":"hello"}`, want: "hello"},
		{name: "delta text", line: `{"type":"unknown","delta":{"text":"world"}}`, want: "world"},
		{name: "delta content", line: `{"type":"unknown","delta":{"content":"fallback"}}`, want: "fallback"},
		{name: "message content", line: `{"type":"unknown","message":{"content":"recovered"}}`, want: "recovered"},
		{name: "no text", line: `{"type":"unknown","value":1}`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandCodeFallbackText([]byte(tt.line)); got != tt.want {
				t.Fatalf("commandCodeFallbackText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func Test_CommandCodeRecoverFromRawBody_handles_legacy_json_envelopes(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "openai message content",
			line: `{"choices":[{"message":{"content":"legacy answer"}}]}`,
			want: "legacy answer",
		},
		{
			name: "typed content parts",
			line: `{"content":[{"type":"text","text":"part one"},{"type":"text","text":"part two"}]}`,
			want: "part one\npart two",
		},
		{
			name: "reasoning delta is not exposed",
			line: `{"type":"reasoning-delta","text":"private chain"}`,
			want: "",
		},
		{
			name: "no recoverable text",
			line: `{"type":"finish","finishReason":"stop"}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandCodeRecoverFromRawBody([][]byte{[]byte(tt.line)}); got != tt.want {
				t.Fatalf("commandCodeRecoverFromRawBody() = %q, want %q", got, tt.want)
			}
		})
	}

	prettyLines := [][]byte{
		[]byte(`{`),
		[]byte(`  "message": {`),
		[]byte(`    "content": "pretty answer"`),
		[]byte(`  }`),
		[]byte(`}`),
	}
	if got := commandCodeRecoverFromRawBody(prettyLines); got != "pretty answer" {
		t.Fatalf("pretty commandCodeRecoverFromRawBody() = %q, want pretty answer", got)
	}
}

func Test_CommandCodeExecutorExecute_recovers_legacy_json_response(t *testing.T) {
	var upstreamURL string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", commandCodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"choices":[{"message":{"content":"fallback answer"}}]}`,
			)),
		}, nil
	}))

	executor := NewCommandCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "user_test"}}
	payload := []byte(`{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"hello"}]}`)
	response, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-pro",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://api.commandcode.ai/alpha/generate" {
		t.Fatalf("upstream URL = %q, want CommandCode generate endpoint", upstreamURL)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != "fallback answer" {
		t.Fatalf("response content = %q, want fallback answer; payload=%s", got, response.Payload)
	}
}

type commandCodeRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f commandCodeRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

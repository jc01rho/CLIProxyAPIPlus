package helps

import (
	"encoding/json"
	"net/http"
	"testing"
)

func confirmedClaudeCodeHeaders() http.Header {
	return http.Header{
		"User-Agent":     {"claude-cli/2.1.220 (external, cli)"},
		"X-App":          {"cli"},
		"Anthropic-Beta": {"claude-code-20250219,interleaved-thinking-2025-05-14"},
	}
}

func TestDetectClaudeCodeRequestRequiresAllFourMessageSignals(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"{\"device_id\":\"abc\",\"session_id\":\"session\"}"}}`)
	detection := DetectClaudeCodeRequest(confirmedClaudeCodeHeaders(), payload, false)

	if !detection.Confirmed || !detection.StrongSignals || !detection.NativeClient {
		t.Fatalf("detection = %#v, want native CLI confirmed", detection)
	}
	if !detection.XAppCLI || !detection.UserAgent || !detection.BetasPresent || !detection.MetadataUserID {
		t.Fatalf("detection signals = %#v, want all present", detection)
	}
}

func TestDetectClaudeCodeRequestRejectsEachMissingMessageSignal(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"user-id"}}`)
	for _, test := range []struct {
		name    string
		headers http.Header
		body    []byte
	}{
		{name: "x-app", headers: http.Header{"User-Agent": {"claude-cli/2.1.220 (external, cli)"}, "Anthropic-Beta": {"claude-code-20250219"}}, body: payload},
		{name: "user-agent", headers: http.Header{"User-Agent": {"curl/8.7.1"}, "X-App": {"cli"}, "Anthropic-Beta": {"claude-code-20250219"}}, body: payload},
		{name: "betas", headers: http.Header{"User-Agent": {"claude-cli/2.1.220 (external, cli)"}, "X-App": {"cli"}}, body: payload},
		{name: "metadata", headers: confirmedClaudeCodeHeaders(), body: []byte(`{"messages":[]}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if detection := DetectClaudeCodeRequest(test.headers, test.body, false); detection.Confirmed {
				t.Fatalf("detection = %#v, want unconfirmed", detection)
			}
		})
	}
}

func TestDetectClaudeCodeRequestClassifiesEntrypoints(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"user-id"}}`)
	for _, test := range []struct {
		name            string
		userAgent       string
		entrypoint      string
		subclient       string
		agentSDKVersion string
		native          bool
	}{
		{name: "cli", userAgent: "claude-cli/2.1.220 (external, cli)", entrypoint: "cli", subclient: "claude-code-cli", native: true},
		{name: "vscode-agent-sdk", userAgent: "claude-cli/2.1.220 (external, claude-vscode, agent-sdk/0.3.220)", entrypoint: "claude-vscode", subclient: "claude-code-vscode", agentSDKVersion: "0.3.220", native: true},
		{name: "sdk-cli", userAgent: "claude-cli/2.1.220 (external, sdk-cli)", entrypoint: "sdk-cli", subclient: "claude-code-cli-sdk", native: true},
		{name: "sdk-ts", userAgent: "claude-cli/2.1.220 (external, sdk-ts, agent-sdk/0.3.220)", entrypoint: "sdk-ts", subclient: "claude-code-sdk-ts", agentSDKVersion: "0.3.220"},
		{name: "sdk-py", userAgent: "claude-cli/2.1.220 (external, sdk-py, agent-sdk/0.1.0)", entrypoint: "sdk-py", subclient: "claude-code-sdk-py", agentSDKVersion: "0.1.0"},
		{name: "desktop", userAgent: "claude-cli/2.1.220 (external, claude-desktop)", entrypoint: "claude-desktop", subclient: "claude-desktop"},
		{name: "desktop-third-party-inference", userAgent: "claude-cli/2.1.220 (external, claude-desktop-3p)", entrypoint: "claude-desktop-3p", subclient: "claude-desktop-3p"},
		{name: "remote", userAgent: "claude-cli/2.1.220 (external, remote)", entrypoint: "remote", subclient: "claude-remote"},
		{name: "github-action", userAgent: "claude-cli/2.1.220 (external, claude-code-github-action)", entrypoint: "claude-code-github-action", subclient: "claude-code-gh-action"},
		{name: "unknown", userAgent: "claude-cli/2.1.220 (external, copied-client)", entrypoint: "copied-client"},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := confirmedClaudeCodeHeaders()
			headers.Set("User-Agent", test.userAgent)
			detection := DetectClaudeCodeRequest(headers, payload, false)
			if !detection.StrongSignals {
				t.Fatalf("detection = %#v, want all CCH strong signals", detection)
			}
			if detection.Confirmed != test.native || detection.NativeClient != test.native {
				t.Fatalf("detection = %#v, want native/confirmed %t", detection, test.native)
			}
			if detection.Entrypoint != test.entrypoint || detection.Subclient != test.subclient || detection.AgentSDKVersion != test.agentSDKVersion {
				t.Fatalf("detection identity = %#v, want entrypoint %q subclient %q agent SDK %q", detection, test.entrypoint, test.subclient, test.agentSDKVersion)
			}
		})
	}
}

func TestDetectClaudeCodeCountTokensAllowsMissingMetadata(t *testing.T) {
	headers := confirmedClaudeCodeHeaders()
	headers.Set("User-Agent", "claude-cli/2.1.220 (external, claude-vscode, agent-sdk/0.3.220)")
	detection := DetectClaudeCodeRequest(headers, []byte(`{"messages":[]}`), true)
	if !detection.Confirmed {
		t.Fatalf("detection = %#v, want confirmed", detection)
	}
	if detection.MetadataUserID {
		t.Fatalf("metadata signal = true, want false: %#v", detection)
	}
	if detection.Subclient != "claude-code-vscode" || detection.AgentSDKVersion != "0.3.220" {
		t.Fatalf("count_tokens identity = %#v, want VSCode Agent SDK", detection)
	}
}

func TestDetectClaudeCodeRequestAcceptsJSONAndLegacyMetadataStrings(t *testing.T) {
	for _, userID := range []string{
		`{"device_id":"abc","account_uuid":"","session_id":"session"}`,
		"user_abc_account__session_session",
	} {
		encodedUserID, errMarshal := json.Marshal(userID)
		if errMarshal != nil {
			t.Fatalf("marshal user_id: %v", errMarshal)
		}
		payload := []byte(`{"metadata":{"user_id":` + string(encodedUserID) + `}}`)
		if detection := DetectClaudeCodeRequest(confirmedClaudeCodeHeaders(), payload, false); !detection.Confirmed {
			t.Fatalf("user_id %q detection = %#v, want confirmed", userID, detection)
		}
	}
}

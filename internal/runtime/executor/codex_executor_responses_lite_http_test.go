package executor

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

// The HTTP /responses upstream must receive the real
// `X-OpenAI-Internal-Codex-Responses-Lite: true` header, and must NOT receive the
// websocket-only client_metadata artifacts the Codex client mirrors onto the frame
// (`ws_request_header_x_openai_internal_codex_responses_lite`,
// `x-codex-ws-stream-request-start-ms`). Reference: cortexkit/openai-auth
// prepareCodexRequest — those keys are only added `if (input.websocket)`.

func TestNormalizeCodexResponsesLiteHTTP_HeaderSignalStripsAndReports(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","client_metadata":{"x-codex-installation-id":"inst","x-codex-ws-stream-request-start-ms":"123"},"input":"hi"}`)
	headers := make(http.Header)
	headers.Set(codexResponsesLiteHeader, "true")

	out, lite := normalizeCodexResponsesLiteHTTP(body, headers)

	if !lite {
		t.Fatalf("expected responses-lite to be detected from header")
	}
	if gjson.GetBytes(out, "client_metadata.x-codex-ws-stream-request-start-ms").Exists() {
		t.Fatalf("ws-only start-ms must be stripped on HTTP path: %s", string(out))
	}
	if got := gjson.GetBytes(out, "client_metadata.x-codex-installation-id").String(); got != "inst" {
		t.Fatalf("installation-id must be preserved, got %q: %s", got, string(out))
	}
}

func TestNormalizeCodexResponsesLiteHTTP_WSMirrorSignalStripsAndReports(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","client_metadata":{"x-codex-installation-id":"inst","ws_request_header_x_openai_internal_codex_responses_lite":"true","x-codex-ws-stream-request-start-ms":"123"},"input":"hi"}`)

	out, lite := normalizeCodexResponsesLiteHTTP(body, nil)

	if !lite {
		t.Fatalf("expected responses-lite to be detected from client_metadata ws mirror")
	}
	if gjson.GetBytes(out, "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite").Exists() {
		t.Fatalf("ws-only responses-lite flag must be stripped on HTTP path: %s", string(out))
	}
	if gjson.GetBytes(out, "client_metadata.x-codex-ws-stream-request-start-ms").Exists() {
		t.Fatalf("ws-only start-ms must be stripped on HTTP path: %s", string(out))
	}
	if got := gjson.GetBytes(out, "client_metadata.x-codex-installation-id").String(); got != "inst" {
		t.Fatalf("installation-id must be preserved, got %q: %s", got, string(out))
	}
}

func TestNormalizeCodexResponsesLiteHTTP_NonLiteStillStripsWSArtifact(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","client_metadata":{"x-codex-installation-id":"inst","x-codex-ws-stream-request-start-ms":"123"},"input":"hi"}`)

	out, lite := normalizeCodexResponsesLiteHTTP(body, nil)

	if lite {
		t.Fatalf("plain (non-lite) request must not be reported responses-lite")
	}
	if gjson.GetBytes(out, "client_metadata.x-codex-ws-stream-request-start-ms").Exists() {
		t.Fatalf("ws-only start-ms is a websocket artifact and must never reach HTTP upstream: %s", string(out))
	}
}

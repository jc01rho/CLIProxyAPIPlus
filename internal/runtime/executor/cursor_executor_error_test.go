package executor

import (
	"errors"
	"strings"
	"testing"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
)

func TestClassifyCursorErrorMapsUnauthenticatedConnectCode(t *testing.T) {
	err := classifyCursorError(&cursorproto.ConnectError{Code: "unauthenticated", Message: "Error"})
	var status cursorStatusErr
	if !errors.As(err, &status) {
		t.Fatalf("classifyCursorError() = %T (%v), want cursorStatusErr", err, err)
	}
	if status.StatusCode() != 401 {
		t.Fatalf("status = %d, want 401", status.StatusCode())
	}
}

func TestClassifyCursorErrorMapsWrappedUnauthenticatedText(t *testing.T) {
	err := classifyCursorError(errors.New("cursor: stream error: Connect error unauthenticated: Error"))
	var status cursorStatusErr
	if !errors.As(err, &status) {
		t.Fatalf("classifyCursorError() = %T (%v), want cursorStatusErr", err, err)
	}
	if status.StatusCode() != 401 {
		t.Fatalf("status = %d, want 401", status.StatusCode())
	}
}

// Given the typed clean-end stream error (a stream that ended cleanly before
// turnEnded),
// When it flows through classifyCursorError toward the client,
// Then it passes through unchanged so the retryable cause stays visible to
// the caller — senpi treats clean-end as a stream-retry cause, not a final
// provider status (stream-retry.ts).
func TestClassifyCursorErrorPassesThroughCleanEndStreamError(t *testing.T) {
	err := classifyCursorError(&cursorRetryableStreamError{
		msg:   "Cursor stream ended before turnEnded",
		cause: cursorStreamRetryCauseCleanEnd,
	})
	var retry *cursorRetryableStreamError
	if !errors.As(err, &retry) {
		t.Fatalf("classifyCursorError() = %T (%v), want the typed retryable error passthrough", err, err)
	}
	if retry.RetryCause() != "clean-end" {
		t.Fatalf("retry cause = %q, want clean-end", retry.RetryCause())
	}
}

func TestCursorAgentHostMatchesSenpi(t *testing.T) {
	if cursorAgentHost != "api2.cursor.sh" {
		t.Fatalf("cursorAgentHost = %q, want api2.cursor.sh", cursorAgentHost)
	}
	if !strings.HasPrefix(cursorAgentURL, "https://api2.cursor.sh") {
		t.Fatalf("cursorAgentURL = %q, want https://api2.cursor.sh", cursorAgentURL)
	}
	if !strings.HasPrefix(cursorClientVersion, "cli-2026.07.") {
		t.Fatalf("cursorClientVersion = %q, want the senpi July 2026 pin", cursorClientVersion)
	}
}

func TestCursorRunHeadersMatchSenpi(t *testing.T) {
	headers := cursorRunHeaders("access::token")
	if got := headers["authorization"]; got != "Bearer token" {
		t.Fatalf("authorization = %q, want Bearer token", got)
	}
	for _, name := range []string{
		"connect-accept-encoding",
		"user-agent",
		"x-original-request-id",
		"traceparent",
		"backend-traceparent",
	} {
		if _, ok := headers[name]; ok {
			t.Fatalf("senpi-incompatible header %q was sent", name)
		}
	}
	if headers["content-type"] != "application/connect+proto" ||
		headers["connect-protocol-version"] != "1" ||
		headers["te"] != "trailers" ||
		headers["x-ghost-mode"] != "true" ||
		headers["x-cursor-client-type"] != "cli" {
		t.Fatalf("protocol headers = %#v", headers)
	}
}

func TestDeriveConversationIDUsesFreshIDsWithoutSession(t *testing.T) {
	first := deriveConversationId("client-key", "", "same prompt")
	second := deriveConversationId("client-key", "", "same prompt")
	if first == second {
		t.Fatalf("conversation IDs collided without a session identity: %q", first)
	}
	if got := deriveConversationId("client-key", "session-1", "same prompt"); got != deriveConversationId("client-key", "session-1", "same prompt") {
		t.Fatalf("conversation ID for the same session was not stable: %q", got)
	}
}

func TestGetCursorFallbackModelsIncludesComposer25(t *testing.T) {
	found := false
	for _, model := range GetCursorFallbackModels() {
		if model.ID == "composer-2.5" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("GetCursorFallbackModels() missing composer-2.5")
	}
}

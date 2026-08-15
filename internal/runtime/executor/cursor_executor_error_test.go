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

func TestCursorAgentHostIsRegionalNotAPI2(t *testing.T) {
	if cursorAgentHost != "agentn.global.api5.cursor.sh" {
		t.Fatalf("cursorAgentHost = %q, want agentn.global.api5.cursor.sh", cursorAgentHost)
	}
	if !strings.HasPrefix(cursorAgentURL, "https://agentn.global.api5.cursor.sh") {
		t.Fatalf("cursorAgentURL = %q, want https://agentn.global.api5.cursor.sh", cursorAgentURL)
	}
	if strings.Contains(cursorAgentURL, "api2.cursor.sh") {
		t.Fatal("agent RPCs must not target api2.cursor.sh")
	}
	if !strings.HasPrefix(cursorClientVersion, "cli-2026.05.") && !strings.HasPrefix(cursorClientVersion, "cli-2026.08.") {
		t.Fatalf("cursorClientVersion = %q, want a May 2026+ CLI pin", cursorClientVersion)
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

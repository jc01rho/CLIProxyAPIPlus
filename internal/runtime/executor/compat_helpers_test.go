package executor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	intlogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	log "github.com/sirupsen/logrus"
)

func TestLogDetailedAPIErrorIncludesFullQuotedRequestAndResponse(t *testing.T) {
	originalOutput := log.StandardLogger().Out
	originalFormatter := log.StandardLogger().Formatter
	originalLevel := log.StandardLogger().Level
	defer func() {
		log.SetOutput(originalOutput)
		log.SetFormatter(originalFormatter)
		log.SetLevel(originalLevel)
	}()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFormatter(&log.TextFormatter{DisableTimestamp: true, DisableLevelTruncation: true})
	log.SetLevel(log.WarnLevel)

	ctx := intlogging.WithRequestID(context.Background(), "abcd1234")
	requestBody := []byte("{\n  \"prompt\": \"hello\"\n}")
	responseBody := []byte("{\n  \"error\": {\n    \"message\": \"boom\"\n  }\n}")

	logDetailedAPIError(ctx, "gemini", "gemma-4-31b-it", "https://example.test", 400, "text/event-stream", requestBody, responseBody)

	output := buf.String()
	if !strings.Contains(output, "Request:") {
		t.Fatalf("expected request section in log, got: %s", output)
	}
	if !strings.Contains(output, "Response:") {
		t.Fatalf("expected response section in log, got: %s", output)
	}
	if !strings.Contains(output, "prompt") || !strings.Contains(output, "hello") {
		t.Fatalf("expected full request payload in log, got: %s", output)
	}
	if !strings.Contains(output, "error") || !strings.Contains(output, "boom") {
		t.Fatalf("expected full response payload in log, got: %s", output)
	}
	if strings.Contains(output, "...[truncated]") {
		t.Fatalf("did not expect truncated marker in log, got: %s", output)
	}
}

func TestFormatDetailedAPILogBodyQuotesAndPreservesFullBody(t *testing.T) {
	body := []byte("{\n  \"error\": {\n    \"message\": \"boom\"\n  }\n}")
	got := formatDetailedAPILogBody(body)
	want := `"{\n  \"error\": {\n    \"message\": \"boom\"\n  }\n}"`
	if got != want {
		t.Fatalf("formatDetailedAPILogBody() = %s, want %s", got, want)
	}
}

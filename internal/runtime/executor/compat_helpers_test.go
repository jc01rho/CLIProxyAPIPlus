package executor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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

	logDetailedAPIError(ctx, nil, "gemini", "gemma-4-31b-it", "https://example.test", 400, "text/event-stream", requestBody, responseBody)

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
	got := formatDetailedAPILogBody(nil, "application/json", body)
	want := `"{\n  \"error\": {\n    \"message\": \"boom\"\n  }\n}"`
	if got != want {
		t.Fatalf("formatDetailedAPILogBody() = %s, want %s", got, want)
	}
}

func TestFormatDetailedAPILogBodyUsesDefaultLimit(t *testing.T) {
	body := []byte(strings.Repeat("a", defaultDetailedAPILogBodyLimit+32))
	got := formatDetailedAPILogBody(nil, "application/json", body)
	if !strings.Contains(got, "...[truncated]") {
		t.Fatalf("expected truncated marker, got: %s", got)
	}
	if strings.Contains(got, strings.Repeat("a", defaultDetailedAPILogBodyLimit+8)) {
		t.Fatalf("expected default limit to truncate body, got: %s", got)
	}
}

func TestFormatDetailedAPILogBodyAllowsUnlimitedWhenNegative(t *testing.T) {
	body := []byte(strings.Repeat("a", defaultDetailedAPILogBodyLimit+32))
	cfg := &config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogLimit: -1}}
	got := formatDetailedAPILogBody(cfg, "application/json", body)
	if strings.Contains(got, "...[truncated]") {
		t.Fatalf("did not expect truncated marker, got: %s", got)
	}
	if !strings.Contains(got, strings.Repeat("a", defaultDetailedAPILogBodyLimit+8)) {
		t.Fatalf("expected full body in log, got: %s", got)
	}
}

func TestFormatDetailedAPILogBodyUsesConfiguredLimit(t *testing.T) {
	body := []byte("abcdefghijklmnopqrstuvwxyz")
	cfg := &config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogLimit: 5}}
	got := formatDetailedAPILogBody(cfg, "application/json", body)
	if got != `"abcde...[truncated]"` {
		t.Fatalf("formatDetailedAPILogBody() = %s, want %s", got, `"abcde...[truncated]"`)
	}
}

func TestFormatDetailedAPILogBodySummaryModeExtractsJSONError(t *testing.T) {
	body := []byte("{\"error\":{\"message\":\"boom\"}}")
	cfg := &config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogFormat: "summary"}}
	got := formatDetailedAPILogBody(cfg, "application/json", body)
	if got != "boom" {
		t.Fatalf("formatDetailedAPILogBody() = %q, want %q", got, "boom")
	}
}

func TestFormatDetailedAPILogBodySummaryModeExtractsHTMLTitle(t *testing.T) {
	body := []byte("<html><head><title>403 Forbidden</title></head><body>x</body></html>")
	cfg := &config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogFormat: "summary"}}
	got := formatDetailedAPILogBody(cfg, "text/html", body)
	if got != "403 Forbidden" {
		t.Fatalf("formatDetailedAPILogBody() = %q, want %q", got, "403 Forbidden")
	}
}

func TestFormatDetailedAPILogBodySummaryModeKeepsPlainText(t *testing.T) {
	body := []byte("plain error text")
	cfg := &config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogFormat: "summary"}}
	got := formatDetailedAPILogBody(cfg, "text/plain", body)
	if got != "plain error text" {
		t.Fatalf("formatDetailedAPILogBody() = %q, want %q", got, "plain error text")
	}
}

func TestFormatDetailedAPILogBodyFullModeStillTruncates(t *testing.T) {
	body := []byte(strings.Repeat("a", defaultDetailedAPILogBodyLimit+8))
	cfg := &config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogFormat: "full"}}
	got := formatDetailedAPILogBody(cfg, "application/json", body)
	if !strings.Contains(got, "...[truncated]") {
		t.Fatalf("expected truncated marker, got: %s", got)
	}
}

func TestFormatDetailedAPILogBodyHandlesNilConfigAndUTF8Boundary(t *testing.T) {
	body := []byte("가나다라마바사")
	cfg := &config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogLimit: 5}}
	got := formatDetailedAPILogBody(cfg, "application/json", body)
	if !strings.Contains(got, "...[truncated]") {
		t.Fatalf("expected truncated marker, got: %s", got)
	}
	if !strings.HasPrefix(got, `"\uac00...[truncated]"`) {
		t.Fatalf("expected UTF-8 safe truncation, got: %s", got)
	}
	if fallback := formatDetailedAPILogBody(nil, "application/json", []byte("ok")); fallback != `"ok"` {
		t.Fatalf("nil cfg fallback = %q, want %q", fallback, `"ok"`)
	}
}

func TestLogDetailedAPIErrorSummaryModeExtractsResponseMessage(t *testing.T) {
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
	cfg := &config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogFormat: "summary"}}
	ctx := intlogging.WithRequestID(context.Background(), "sum123")
	requestBody := []byte("{\"prompt\":\"hello\"}")
	responseBody := []byte("{\"error\":{\"message\":\"boom\"}}")
	logDetailedAPIError(ctx, cfg, "gemini", "gemma", "https://example.test", 400, "application/json", requestBody, responseBody)
	output := buf.String()
	if !strings.Contains(output, "Response: boom") {
		t.Fatalf("expected summarized response body, got: %s", output)
	}
	if strings.Contains(output, "\"error\"") {
		t.Fatalf("expected summary mode to avoid full JSON response, got: %s", output)
	}
}

func TestFormatDetailedAPILogBodyRequestSanitizationRemovesMessages(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"secret prompt"}],"max_tokens":128}`)
	got := formatDetailedAPILogBody(&config.Config{SDKConfig: config.SDKConfig{DetailedAPIErrorBodyLogFormat: "full", DetailedAPIErrorBodyLogLimit: -1}}, "application/json", sanitizeDetailedAPIRequestBody(body))
	if strings.Contains(got, `messages`) || strings.Contains(got, `secret prompt`) {
		t.Fatalf("expected messages to be removed from request body log, got: %s", got)
	}
	if !strings.Contains(got, `claude-sonnet-4-5`) || !strings.Contains(got, `max_tokens`) {
		t.Fatalf("expected non-message fields to remain in sanitized request log, got: %s", got)
	}
}

package logging

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestLogFormatterDoesNotExpandUnrelatedFields(t *testing.T) {
	for _, source := range []string{"", "other-fallback"} {
		var output bytes.Buffer
		logger := log.New()
		logger.SetOutput(&output)
		logger.SetFormatter(&LogFormatter{})
		logger.WithFields(log.Fields{
			"provider": "fixture-provider", "model": "fixture-model",
			"requested_model": "PRIVATE_REQUESTED_MODEL", "outcome": "PRIVATE_OUTCOME", "elapsed_ms": 123,
			"fallback_source": source, "fallback_trigger_error": "PRIVATE_DIAGNOSTIC",
		}).Info("unrelated event fixture")
		line := output.String()
		for _, key := range []string{"requested_model", "outcome", "elapsed_ms", "fallback_source", "fallback_trigger_error"} {
			if strings.Contains(line, key+"=") {
				t.Errorf("unrelated event rendered %s: %q", key, line)
			}
		}
		if !strings.Contains(line, " provider=fixture-provider model=fixture-model") {
			t.Errorf("existing common fields changed: %q", line)
		}
	}
}

func TestLogFormatterFallbackDiagnostics(t *testing.T) {
	var output bytes.Buffer
	logger := log.New()
	logger.SetOutput(&output)
	logger.SetFormatter(&LogFormatter{})
	logger.SetLevel(log.InfoLevel)
	fields := log.Fields{
		"request_id":              "fallback-formatter",
		"requested_model":         "original",
		"fallback_trigger_model":  "middle",
		"selected_fallback_model": "target",
		"fallback_source":         "fallback-chain",
		"fallback_trigger_status": 503,
		"fallback_trigger_error":  "overloaded\r\n\"outcome=success\"\t\x1b[31m",
		"fallback_result_status":  429,
		"fallback_result_error":   "quota\nlimit",
		"outcome":                 "error",
		"elapsed_ms":              int64(12),
		"private_payload":         "DO_NOT_RENDER_PAYLOAD",
		"authorization":           "DO_NOT_RENDER_AUTHORIZATION",
		"downstream_api_key":      "DO_NOT_RENDER_API_KEY",
	}
	logger.WithFields(fields).Info("fallback diagnostic fixture")
	line := output.String()
	for _, key := range []string{
		"fallback_trigger_error", "fallback_trigger_status", "fallback_trigger_model",
		"requested_model", "selected_fallback_model", "fallback_source",
		"fallback_result_status", "fallback_result_error", "outcome", "elapsed_ms",
	} {
		value := ""
		switch v := fields[key].(type) {
		case string:
			value = strconv.Quote(v)
		case int:
			value = strconv.Itoa(v)
		case int64:
			value = strconv.FormatInt(v, 10)
		}
		if !strings.Contains(line, " "+key+"="+value) {
			t.Errorf("formatted output missing %s=%s: %q", key, value, line)
		}
	}
	if !strings.Contains(line, "[fallback-formatter] [info ") {
		t.Errorf("missing request ID or Info level: %q", line)
	}
	if strings.Count(line, "\n") != 1 || strings.ContainsAny(line, "\r\t\x1b") {
		t.Errorf("unescaped control character in formatted output: %q", line)
	}
	if strings.Contains(line, "DO_NOT_RENDER_") {
		t.Errorf("formatter dumped non-allowlisted fields: %q", line)
	}
}

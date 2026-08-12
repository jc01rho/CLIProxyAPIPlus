package thinking_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	log "github.com/sirupsen/logrus"
)

func TestValidateConfigWithContextLogsRawAPIKeyAndRequestID(t *testing.T) {
	var logBuffer bytes.Buffer
	previousOutput := log.StandardLogger().Out
	previousLevel := log.GetLevel()
	log.SetOutput(&logBuffer)
	log.SetLevel(log.WarnLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
	})

	ctx := thinking.WithRequestLogMetadata(context.Background(), "request-1234", "sk-raw-configured-key")
	modelInfo := &registry.ModelInfo{
		ID: "gpt-5.6-sol",
		Thinking: &registry.ThinkingSupport{
			Min:         0,
			Max:         0,
			ZeroAllowed: false,
		},
	}

	_, err := thinking.ValidateConfigWithContext(ctx, thinking.ThinkingConfig{
		Mode:   thinking.ModeBudget,
		Budget: 0,
	}, modelInfo, "openai-response", "codex", false)
	if err != nil {
		t.Fatalf("ValidateConfigWithContext() error = %v", err)
	}

	output := logBuffer.String()
	t.Logf("thinking validation WARN: %s", output)
	for _, expected := range []string{
		"thinking: budget zero not allowed",
		"api_key=sk-raw-configured-key",
		"request_id=request-1234",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected %q in log output: %s", expected, output)
		}
	}
}

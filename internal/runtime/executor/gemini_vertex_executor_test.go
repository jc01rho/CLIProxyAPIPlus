package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestGeminiVertexExecutorExecuteDropsThinkingBudgetForFlashLiteWhenLevelExists(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiVertexExecutor(&config.Config{
		Payload: config.PayloadConfig{Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gemini-3.1-flash-lite", Protocol: "gemini"}},
			Params: map[string]any{
				"generationConfig.thinkingConfig.thinkingLevel":  "high",
				"generationConfig.thinkingConfig.thinkingBudget": 1024,
			},
		}}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key": "test-key", "base_url": server.URL,
	}}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-flash-lite",
		Payload: []byte(`{"model":"gemini-3.1-flash-lite","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}

	if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(upstreamBody, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
		t.Fatalf("upstream thinkingLevel = %q, want high. Body: %s", got, string(upstreamBody))
	}
	if gjson.GetBytes(upstreamBody, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Fatalf("upstream thinkingBudget exists, want removed. Body: %s", string(upstreamBody))
	}
}

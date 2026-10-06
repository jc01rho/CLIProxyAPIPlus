package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestCapMaxTokens(t *testing.T) {
	cap := func(model string) int {
		if model == "glm-5.3-flash" {
			return 131072
		}
		return 0
	}
	exec := &ClaudeExecutor{upstreamMaxTokensCap: cap}
	tests := []struct {
		name  string
		exec  *ClaudeExecutor
		model string
		body  string
		want  string
	}{
		{"above cap is lowered", exec, "glm-5.3-flash", `{"max_tokens":200000}`, "131072"},
		{"at cap is untouched", exec, "glm-5.3-flash", `{"max_tokens":131072}`, "131072"},
		{"below cap is untouched", exec, "glm-5.3-flash", `{"max_tokens":4096}`, "4096"},
		{"absent stays absent", exec, "glm-5.3-flash", `{"messages":[]}`, ""},
		{"other model untouched", exec, "other-model", `{"max_tokens":200000}`, "200000"},
		{"no cap function untouched", &ClaudeExecutor{}, "glm-5.3-flash", `{"max_tokens":200000}`, "200000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.exec.capMaxTokens(context.Background(), []byte(tt.body), tt.model)
			value := gjson.GetBytes(got, "max_tokens")
			if tt.want == "" {
				if value.Exists() {
					t.Fatalf("max_tokens = %s, want absent", value.Raw)
				}
				return
			}
			if value.Raw != tt.want {
				t.Fatalf("max_tokens = %s, want %s", value.Raw, tt.want)
			}
		})
	}
}

func TestZcodeMaxTokensCap(t *testing.T) {
	for _, model := range []string{"glm-5.3-flash", "GLM-5.3-Flash", "glm-5.2", "a-live-discovered-glm"} {
		if got := zcodeMaxTokensCap(model); got != 131072 {
			t.Errorf("zcodeMaxTokensCap(%q) = %d, want 131072", model, got)
		}
	}
}

func zcodeCappedRequest(t *testing.T, cfg *config.Config, stream bool) int64 {
	t.Helper()
	t.Setenv("ZCODE_DEVICE_ID", "device-max-tokens")
	server, seen := newZcodeCaptureServer(t)
	t.Setenv("ZCODE_ANTHROPIC_BASE_URL", server.URL)

	exec := NewZcodeExecutor(cfg)
	auth := &cliproxyauth.Auth{Provider: "zcode", Attributes: map[string]string{
		"api_key": "key-1.secret",
		"email":   "user@example.com",
	}}
	req := cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","max_tokens":200000,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`),
	}
	if stream {
		result, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{Stream: true})
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
	} else if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	value := gjson.GetBytes(seen.body, "max_tokens")
	if !value.Exists() {
		t.Fatalf("upstream body has no max_tokens: %s", seen.body)
	}
	return value.Int()
}

func TestZcodeCapsMaxTokensOnTheWire(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "non-stream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			if got := zcodeCappedRequest(t, nil, stream); got != 131072 {
				t.Fatalf("upstream max_tokens = %d, want 131072", got)
			}
		})
	}
}

func TestZcodeCapWinsOverPayloadRule(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "glm-5.3-flash", Protocol: "claude"}},
		Params: map[string]any{"max_tokens": 300000},
	}}}}
	for _, stream := range []bool{false, true} {
		if got := zcodeCappedRequest(t, cfg, stream); got != 131072 {
			t.Fatalf("stream=%v upstream max_tokens = %d, want 131072 despite the payload rule", stream, got)
		}
	}
}

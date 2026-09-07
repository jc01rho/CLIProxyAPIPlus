package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// codexImageGenUpstream returns a stub Codex upstream plus a pointer to the last
// captured request body so tests can assert on the outgoing tools array.
func codexImageGenUpstream(t *testing.T) (*httptest.Server, *[]byte) {
	t.Helper()
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	t.Cleanup(server.Close)
	return server, &gotBody
}

func hasImageGenerationTool(body []byte) bool {
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() == "image_generation" {
			return true
		}
	}
	return false
}

func TestCodexExecutorPerKeyDisableImageGenerationSuppressesInjection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		globalMode config.DisableImageGenerationMode
		override   string
		wantTool   bool
	}{
		{name: "global off with no override injects", globalMode: config.DisableImageGenerationOff, wantTool: true},
		{name: "per-key chat suppresses", globalMode: config.DisableImageGenerationOff, override: "chat", wantTool: false},
		{name: "per-key true suppresses", globalMode: config.DisableImageGenerationOff, override: "true", wantTool: false},
		{name: "per-key passthrough suppresses injection", globalMode: config.DisableImageGenerationOff, override: "passthrough", wantTool: false},
		{name: "per-key false re-enables under global chat", globalMode: config.DisableImageGenerationChat, override: "false", wantTool: true},
		{name: "global chat with no override suppresses", globalMode: config.DisableImageGenerationChat, wantTool: false},
		{name: "invalid override falls back to global off", globalMode: config.DisableImageGenerationOff, override: "sometimes", wantTool: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, gotBody := codexImageGenUpstream(t)

			cfg := &config.Config{}
			cfg.DisableImageGeneration = tc.globalMode
			exec := NewCodexExecutor(cfg)

			auth := &cliproxyauth.Auth{
				Provider: "codex",
				Attributes: map[string]string{
					"api_key":   "test",
					"base_url":  server.URL,
					"plan_type": "pro",
				},
			}
			if tc.override != "" {
				auth.Metadata = map[string]any{"disable_image_generation": tc.override}
			}

			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-6-astra",
				Payload: []byte(`{"model":"gpt-6-astra","input":"hello"}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got := hasImageGenerationTool(*gotBody); got != tc.wantTool {
				t.Fatalf("image_generation tool present = %t, want %t; body=%s", got, tc.wantTool, *gotBody)
			}
		})
	}
}

func TestCodexExecutorPerKeyOverrideIsScopedToThatCredential(t *testing.T) {
	cfg := &config.Config{}
	exec := NewCodexExecutor(cfg)

	newAuth := func(baseURL string, metadata map[string]any) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"api_key":   "test",
				"base_url":  baseURL,
				"plan_type": "pro",
			},
			Metadata: metadata,
		}
	}

	disabledServer, disabledBody := codexImageGenUpstream(t)
	enabledServer, enabledBody := codexImageGenUpstream(t)

	req := cliproxyexecutor.Request{
		Model:   "gpt-6-astra",
		Payload: []byte(`{"model":"gpt-6-astra","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}

	if _, err := exec.Execute(context.Background(), newAuth(disabledServer.URL, map[string]any{"disable_image_generation": "chat"}), req, opts); err != nil {
		t.Fatalf("Execute() disabled credential error = %v", err)
	}
	if _, err := exec.Execute(context.Background(), newAuth(enabledServer.URL, nil), req, opts); err != nil {
		t.Fatalf("Execute() default credential error = %v", err)
	}

	if hasImageGenerationTool(*disabledBody) {
		t.Errorf("overridden credential should not receive image_generation: %s", *disabledBody)
	}
	if !hasImageGenerationTool(*enabledBody) {
		t.Errorf("default credential should still receive image_generation: %s", *enabledBody)
	}
}

// A client that explicitly sends image_generation must also be stripped for a
// credential whose per-key disable-image-generation is active. Injection gating
// alone is not enough: ApplyPayloadConfig only sees the global value.
func TestCodexExecutorPerKeyStripsClientSentImageGenerationTool(t *testing.T) {
	for _, tc := range []struct {
		name     string
		override string
		wantTool bool
	}{
		{name: "no override keeps client tool", override: "", wantTool: true},
		{name: "per-key chat strips client tool", override: "chat", wantTool: false},
		{name: "per-key true strips client tool", override: "true", wantTool: false},
		{name: "per-key passthrough keeps client tool", override: "passthrough", wantTool: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, gotBody := codexImageGenUpstream(t)
			exec := NewCodexExecutor(&config.Config{})

			auth := &cliproxyauth.Auth{
				Provider: "codex",
				Attributes: map[string]string{
					"api_key":   "test",
					"base_url":  server.URL,
					"plan_type": "pro",
				},
			}
			if tc.override != "" {
				auth.Metadata = map[string]any{"disable_image_generation": tc.override}
			}

			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-6-astra",
				Payload: []byte(`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"image_generation"}],"tool_choice":{"type":"image_generation"}}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got := hasImageGenerationTool(*gotBody); got != tc.wantTool {
				t.Fatalf("image_generation tool = %t, want %t; body=%s", got, tc.wantTool, *gotBody)
			}
			if !tc.wantTool && gjson.GetBytes(*gotBody, "tool_choice").Exists() {
				t.Fatalf("tool_choice should be removed; body=%s", *gotBody)
			}
		})
	}
}

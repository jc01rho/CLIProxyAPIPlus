package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	"github.com/tidwall/gjson"
)

// Reproduces an Ollama (openai-compatible) model that has been resolved to a
// registered, non-user-defined ModelInfo (e.g. discovered via the provider's
// /v1/models listing) whose declared Levels do not include "xhigh"/"max".
//
// Before the fix, such a model went through the strict registry validation
// path (ValidateConfigWithContext), which rejected "xhigh"/"max" outright with
// ErrLevelNotSupported instead of clamping them like every other
// openai-compatible/user-defined model does. ApplyThinking's defensive
// contract returns the original body verbatim on validation failure, so the
// error was easy to swallow upstream and the client got no indication that
// their requested effort was dropped.
//
// The fix routes every provider whose key is prefixed with
// "openai-compatible-" through the user-defined application path, which
// clamps unsupported levels to the nearest supported one instead of failing.
func TestOllamaOpenAICompatResolvedModelClampsUnsupportedXHighMax(t *testing.T) {
	provider := "openai-compatible-ollama-openaicompatible"
	models := []*registry.ModelInfo{{
		ID:          "qwen3",
		Object:      "model",
		Type:        "openai",
		UserDefined: false,
		Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
	}}
	reg := registry.GetGlobalRegistry()
	clientID := "test-ollama-resolved-clamp"
	reg.RegisterClient(clientID, provider, models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	for _, tt := range []struct {
		effort string
		want   string
	}{
		{"xhigh", "high"},
		{"max", "high"},
	} {
		t.Run(tt.effort, func(t *testing.T) {
			body := []byte(`{"model":"qwen3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"` + tt.effort + `"}`)
			out, err := thinking.ApplyThinking(body, "qwen3", "openai", "openai", provider)
			if err != nil {
				t.Fatalf("ApplyThinking returned error: %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning_effort").String(); got != tt.want {
				t.Fatalf("reasoning_effort = %q, want %q; body=%s", got, tt.want, out)
			}
		})
	}
}

// Reproduces an Ollama (openai-compatible) model whose registry entry
// explicitly declares support for "xhigh" and "max" reasoning efforts. These
// values must be forwarded to the upstream verbatim, not silently downgraded.
func TestOllamaOpenAICompatResolvedModelForwardsSupportedXHighMax(t *testing.T) {
	provider := "openai-compatible-ollama-openaicompatible"
	models := []*registry.ModelInfo{{
		ID:          "qwen3-big",
		Object:      "model",
		Type:        "openai",
		UserDefined: false,
		Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh", "max"}},
	}}
	reg := registry.GetGlobalRegistry()
	clientID := "test-ollama-resolved-forward"
	reg.RegisterClient(clientID, provider, models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	for _, effort := range []string{"xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			body := []byte(`{"model":"qwen3-big","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"` + effort + `"}`)
			out, err := thinking.ApplyThinking(body, "qwen3-big", "openai", "openai", provider)
			if err != nil {
				t.Fatalf("ApplyThinking returned error: %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning_effort").String(); got != effort {
				t.Fatalf("reasoning_effort = %q, want %q; body=%s", got, effort, out)
			}
		})
	}
}

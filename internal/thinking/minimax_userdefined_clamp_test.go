package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	"github.com/tidwall/gjson"
)

// Reproduces openai-compatible (ollama) minimax-m3 with reasoning_effort=xhigh.
// minimax-m3 is not in the static registry, so it is registered as a user-defined
// openai-compat model with Levels=[low,medium,high]. The user-defined path must
// clamp the unsupported "xhigh" level to the nearest supported level ("high")
// instead of passing it through, otherwise upstream rejects the request:
//   invalid reasoning value: 'xhigh' (must be "high","medium","low","max","none")
func TestOpenAICompatUserDefinedClampsXHighToHigh(t *testing.T) {
	provider := "openai-compatible-ollama-openaicompatible"
	models := []*registry.ModelInfo{{
		ID:          "minimax-m3",
		Object:      "model",
		Type:        "openai",
		UserDefined: true,
		Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
	}}
	reg := registry.GetGlobalRegistry()
	clientID := "test-minimax-m3-clamp"
	reg.RegisterClient(clientID, provider, models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	body := []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`)
	out, err := thinking.ApplyThinking(body, "minimax-m3", "openai", "openai", provider)
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want high", got)
	}
}

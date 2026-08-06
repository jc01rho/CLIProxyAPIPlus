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
//
//	invalid reasoning value: 'xhigh' (must be "high","medium","low","max","none")
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

func TestOpenAICompatUserDefinedMapsThinkingTypeLevels(t *testing.T) {
	provider := "openai-compatible-nvidia-nvapi"
	models := []*registry.ModelInfo{{
		ID:          "minimaxai/minimax-m3",
		Object:      "model",
		Type:        "openai",
		UserDefined: true,
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"enable", "disable", "adaptive"},
		},
	}}
	reg := registry.GetGlobalRegistry()
	clientID := "test-minimax-m3-thinking-type-levels"
	reg.RegisterClient(clientID, provider, models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	tests := []struct {
		name  string
		model string
		body  string
		want  string
	}{
		{
			name:  "high effort maps to adaptive",
			model: "minimaxai/minimax-m3",
			body:  `{"model":"minimaxai/minimax-m3","reasoning_effort":"high"}`,
			want:  "adaptive",
		},
		{
			name:  "none effort maps to disabled",
			model: "minimaxai/minimax-m3",
			body:  `{"model":"minimaxai/minimax-m3","reasoning_effort":"none"}`,
			want:  "disabled",
		},
		{
			name:  "enable suffix maps to enabled",
			model: "minimaxai/minimax-m3(enable)",
			body:  `{"model":"minimaxai/minimax-m3","messages":[{"role":"user","content":"hi"}]}`,
			want:  "enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.ApplyThinking([]byte(tt.body), tt.model, "openai", "openai", provider)
			if err != nil {
				t.Fatalf("ApplyThinking returned error: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != tt.want {
				t.Fatalf("thinking.type = %q, want %q; body=%s", got, tt.want, out)
			}
			if gjson.GetBytes(out, "reasoning_effort").Exists() {
				t.Fatalf("reasoning_effort must be removed when thinking.type is selected; body=%s", out)
			}
		})
	}
}

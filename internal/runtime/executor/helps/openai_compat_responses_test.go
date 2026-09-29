package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCompatResponsesSameUpstreamAliases(t *testing.T) {
	cfg := &config.OpenAICompatibility{Models: []config.OpenAICompatibilityModel{
		{Name: "upstream", Alias: "chat", SupportedEndpoints: []string{"/chat/completions"}},
		{Name: "upstream", Alias: "responses", SupportedEndpoints: []string{"/responses"}},
	}}
	if !CompatModelResponsesOnly(cfg, "upstream", "responses", false) {
		t.Fatal("resolved upstream must retain the requested alias endpoint")
	}
	if CompatModelResponsesOnly(cfg, "upstream", "chat", false) {
		t.Fatal("Responses alias must not affect the Chat alias")
	}
}

func TestCompatResponsesPreservesClaudeParallelRestriction(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`)
	_, output, _ := TranslateCompatResponsesRequest(context.Background(), nil, &config.Config{}, sdktranslator.FormatClaude, "m", input, input, false, false)
	parallel := gjson.GetBytes(output, "parallel_tool_calls")
	if !parallel.Exists() || parallel.Bool() {
		t.Fatalf("parallel tool prohibition lost: %s", output)
	}
}

func TestCompatResponsesOmitsUnrequestedCodexDefaults(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":41,"temperature":0.2}`)
	_, output, _ := TranslateCompatResponsesRequest(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAI, "m", input, input, false, false)
	for _, key := range []string{"reasoning.effort", "store", "include", "parallel_tool_calls"} {
		if gjson.GetBytes(output, key).Exists() {
			t.Errorf("unrequested Codex default %s: %s", key, output)
		}
	}
	if gjson.GetBytes(output, "max_output_tokens").Int() != 41 || gjson.GetBytes(output, "temperature").Float() != 0.2 {
		t.Fatalf("generation parameters lost: %s", output)
	}
}

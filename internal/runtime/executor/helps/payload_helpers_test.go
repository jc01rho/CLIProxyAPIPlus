package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

func TestApplyPayloadConfigWithRoot_RemovesThinkingBudgetForGemmaModel(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{{Name: "gemma-4-31b-it", Protocol: "gemini"}},
					Params: []string{"generationConfig.thinkingConfig.thinkingBudget"},
				},
			},
		},
	}

	payload := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":1024,"includeThoughts":true}}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gemma-4-31b-it", "gemini", "", payload, nil, "", "")

	if gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Fatalf("thinkingBudget should be removed, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts"); !got.Exists() || !got.Bool() {
		t.Fatalf("includeThoughts should remain true, body=%s", string(out))
	}
}

func TestApplyPayloadConfigWithRoot_RemovesThinkingBudgetForRequestedGemmaAlias(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{{Name: "gemma4"}},
					Params: []string{"generationConfig.thinkingConfig.thinkingBudget"},
				},
			},
		},
	}

	payload := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":2048,"includeThoughts":true}}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gemma-4-31b-it", "gemini", "", payload, nil, "gemma4", "")

	if gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Fatalf("thinkingBudget should be removed for requested alias, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts"); !got.Exists() || !got.Bool() {
		t.Fatalf("includeThoughts should remain true, body=%s", string(out))
	}
}

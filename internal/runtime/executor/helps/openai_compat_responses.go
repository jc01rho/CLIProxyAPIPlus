package helps

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CompatModelResponsesOnly uses the selected provider and resolved upstream name,
// never globally registered aliases (which may belong to another provider).
func CompatModelResponsesOnly(compat *config.OpenAICompatibility, model, requested string, zenDefault bool) bool {
	if compat == nil {
		return false
	}
	var selected *config.OpenAICompatibilityModel
	for i := range compat.Models {
		entry := &compat.Models[i]
		if requested != "" && strings.EqualFold(strings.TrimSpace(entry.Name), model) && strings.EqualFold(strings.TrimSpace(entry.Alias), requested) {
			selected = entry
			break
		}
	}
	for i := range compat.Models {
		if selected != nil {
			break
		}
		if strings.EqualFold(strings.TrimSpace(compat.Models[i].Name), model) {
			selected = &compat.Models[i]
			break
		}
	}
	if selected == nil {
		for _, candidate := range []string{model, requested} {
			for i := range compat.Models {
				if candidate != "" && strings.EqualFold(strings.TrimSpace(compat.Models[i].Alias), candidate) {
					selected = &compat.Models[i]
					break
				}
			}
			if selected != nil {
				break
			}
		}
	}
	if selected == nil {
		return false
	}
	var responses, chat, explicit bool
	for _, endpoint := range selected.SupportedEndpoints {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			continue
		}
		explicit = true
		if !strings.HasPrefix(endpoint, "/") {
			endpoint = "/" + endpoint
		}
		responses = responses || endpoint == "/responses"
		chat = chat || endpoint == "/chat/completions"
	}
	if !explicit {
		return zenDefault
	}
	return responses && !chat
}

// TranslateCompatResponsesRequest preserves native Responses payloads verbatim
// through the same-format pipeline. Other clients reuse existing Responses-wire
// converters, restoring public API parameters omitted by Codex-specific defaults.
func TranslateCompatResponsesRequest(ctx context.Context, headers http.Header, cfg *config.Config, from sdktranslator.Format, model string, original, payload []byte, stream, isCompat bool) ([]byte, []byte, bool) {
	to := sdktranslator.FormatOpenAIResponse
	if from != to && !sdktranslator.HasRequestTransformer(from, to) {
		to = sdktranslator.FormatCodex
	}
	baseline, working, changed := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, headers, cfg, from, to, model, original, payload, stream, isCompat)
	if to == sdktranslator.FormatCodex {
		baseline = restoreCompatResponsesParameters(baseline, original, from)
		working = restoreCompatResponsesParameters(working, payload, from)
	}
	return baseline, working, changed
}

func restoreCompatResponsesParameters(translated, source []byte, from sdktranslator.Format) []byte {
	for _, field := range []string{"store", "parallel_tool_calls", "include", "temperature", "top_p", "max_output_tokens", "previous_response_id", "metadata", "truncation", "service_tier", "user", "prompt_cache_key", "prompt_cache_retention"} {
		if field == "parallel_tool_calls" && from == sdktranslator.FormatClaude && gjson.GetBytes(source, "tool_choice.disable_parallel_tool_use").Exists() {
			continue
		}
		if value := gjson.GetBytes(source, field); value.Exists() {
			translated, _ = sjson.SetRawBytes(translated, field, []byte(value.Raw))
		} else if field == "store" || field == "parallel_tool_calls" || field == "include" {
			translated, _ = sjson.DeleteBytes(translated, field)
		}
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		if value := gjson.GetBytes(source, field); value.Exists() {
			translated, _ = sjson.SetRawBytes(translated, "max_output_tokens", []byte(value.Raw))
		}
	}
	if from == sdktranslator.FormatOpenAI && !gjson.GetBytes(source, "reasoning_effort").Exists() {
		translated, _ = sjson.DeleteBytes(translated, "reasoning.effort")
	}
	return translated
}

// CompatResponsesTranslationFormat selects an existing response translator.
// Codex and public Responses share the response wire schema; this does not use
// the Codex executor or its native-request normalization.
func CompatResponsesTranslationFormat(responseFormat sdktranslator.Format) sdktranslator.Format {
	if responseFormat == sdktranslator.FormatOpenAIResponse || sdktranslator.HasNonStreamResponseTransformer(responseFormat, sdktranslator.FormatOpenAIResponse) {
		return sdktranslator.FormatOpenAIResponse
	}
	return sdktranslator.FormatCodex
}

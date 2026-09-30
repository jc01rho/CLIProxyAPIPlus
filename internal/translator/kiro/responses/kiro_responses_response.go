package responses

import (
	"bytes"
	"context"

	kiroopenai "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/kiro/openai"
	chatresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
)

type streamState struct {
	chatParam any
	response  any
	completed bool
}

// ConvertKiroNonStreamToOpenAIResponses reconstructs the Responses envelope
// from Kiro's Claude-compatible intermediate through the existing chat facade.
func ConvertKiroNonStreamToOpenAIResponses(ctx context.Context, model string, originalRequest, request, rawResponse []byte, _ *any) []byte {
	chat := kiroopenai.ConvertKiroNonStreamToOpenAI(ctx, model, originalRequest, request, rawResponse, new(any))
	return chatresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(ctx, model, originalRequest, originalRequest, chat, new(any))
}

// ConvertKiroStreamToOpenAIResponses translates the existing Kiro-to-chat SSE
// facade into item-centered Responses events. Kiro's message_stop has no chat
// chunk, so it supplies the terminal [DONE] marker required by the facade.
// Ported from kiro-lb src/stream_responses.rs (1581af9).
func ConvertKiroStreamToOpenAIResponses(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) [][]byte {
	if *param == nil {
		*param = &streamState{}
	}
	state := (*param).(*streamState)
	if isKiroMessageStop(rawResponse) {
		if state.completed {
			return nil
		}
		state.completed = true
		return chatresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, originalRequest, originalRequest, []byte("[DONE]"), &state.response)
	}
	chatChunks := kiroopenai.ConvertKiroStreamToOpenAI(ctx, model, originalRequest, request, rawResponse, &state.chatParam)
	var output [][]byte
	for _, chunk := range chatChunks {
		output = append(output, chatresponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, originalRequest, originalRequest, chunk, &state.response)...)
	}
	return output
}

func isKiroMessageStop(raw []byte) bool {
	return bytes.Contains(raw, []byte("event: message_stop")) || bytes.Contains(raw, []byte(`"type":"message_stop"`))
}

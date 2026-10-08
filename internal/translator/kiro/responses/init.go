// Package responses translates OpenAI Responses requests and responses for Kiro.
package responses

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Kiro,
		func(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
			return ConvertOpenAIResponsesRequestToKiro(modelName, inputRawJSON, stream), nil
		},
		interfaces.TranslateResponse{
			Stream:    ConvertKiroStreamToOpenAIResponses,
			NonStream: ConvertKiroNonStreamToOpenAIResponses,
		},
	)
}

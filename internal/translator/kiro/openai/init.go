// Package openai provides translation between OpenAI Chat Completions and Kiro formats.
package openai

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI, // source format
		Kiro,   // target format
		func(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
			return ConvertOpenAIRequestToKiro(modelName, inputRawJSON, stream), nil
		},
		interfaces.TranslateResponse{
			Stream:    ConvertKiroStreamToOpenAI,
			NonStream: ConvertKiroNonStreamToOpenAI,
		},
	)
}

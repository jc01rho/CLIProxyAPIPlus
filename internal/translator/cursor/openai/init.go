package openai

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Cursor,
		func(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
			return ConvertOpenAIRequestToCursor(modelName, inputRawJSON, stream), nil
		},
		interfaces.TranslateResponse{
			Stream:    ConvertCursorResponseToOpenAI,
			NonStream: ConvertCursorResponseToOpenAINonStream,
		},
	)
}

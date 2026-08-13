package openai

import (
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Cursor,
		ConvertOpenAIRequestToCursor,
		interfaces.TranslateResponse{
			Stream:    ConvertCursorResponseToOpenAI,
			NonStream: ConvertCursorResponseToOpenAINonStream,
		},
	)
}

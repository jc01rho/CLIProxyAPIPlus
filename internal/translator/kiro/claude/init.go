// Package claude provides translation between Kiro and Claude formats.
package claude

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		Kiro,
		func(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
			return ConvertClaudeRequestToKiro(modelName, inputRawJSON, stream), nil
		},
		interfaces.TranslateResponse{
			Stream:    ConvertKiroStreamToClaude,
			NonStream: ConvertKiroNonStreamToClaude,
		},
	)
}

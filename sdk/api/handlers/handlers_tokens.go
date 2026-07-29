package handlers

import (
	"strings"

	executorhelps "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tiktoken-go/tokenizer"
)

func maybeAttachEstimatedInputTokens(meta map[string]any, format sdktranslator.Format, model string, rawJSON []byte) {
	if meta == nil || len(rawJSON) == 0 {
		return
	}

	codec, err := requestTokenizerForModel(model)
	if err != nil {
		return
	}

	var count int64
	if format == sdktranslator.FormatClaude {
		count, err = executorhelps.CountClaudeChatTokens(codec, rawJSON)
	} else {
		count, err = executorhelps.CountOpenAIChatTokens(codec, rawJSON)
	}
	if err != nil || count <= 0 {
		return
	}
	meta[coreexecutor.EstimatedInputTokensMetadataKey] = count
}

func requestTokenizerForModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	if sanitized == "" || strings.Contains(sanitized, "claude") || strings.HasPrefix(sanitized, "kiro-") || strings.HasPrefix(sanitized, "amazonq-") {
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
	return executorhelps.TokenizerForModel(sanitized)
}

package helps

import "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"

// ApplyThinkingWithSourcePayload preserves summary visibility from the original
// client payload while applying thinking configuration to its translated target
// payload. A target representation alone can lose an explicit disabled summary
// before a model suffix changes Claude thinking from disabled to adaptive.
func ApplyThinkingWithSourcePayload(body, sourcePayload []byte, model, fromFormat, toFormat, providerKey string) ([]byte, error) {
	summary := thinking.ExtractSummaryConfig(sourcePayload, fromFormat)
	return thinking.ApplyThinkingWithSummary(body, model, fromFormat, toFormat, providerKey, summary)
}

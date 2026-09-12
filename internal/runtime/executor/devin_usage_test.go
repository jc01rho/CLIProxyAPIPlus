package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestDevinUsageToDetail pins the field mapping from the upstream
// ModelUsageStats onto the shared usage.Detail. Devin usage was extracted for
// the client payload but never published, so the access log showed "(streamed)"
// and the usage keeper recorded nothing. This is the conversion the reporter
// now publishes.
func TestDevinUsageToDetail(t *testing.T) {
	u := devinUsage{
		PromptTokens:     1200,
		CompletionTokens: 340,
		CachedTokens:     800,
		CacheWriteTokens: 50,
		ReasoningTokens:  90,
	}

	got := devinUsageToDetail(u)

	want := usage.Detail{
		InputTokens:         1200,
		OutputTokens:        340,
		ReasoningTokens:     90,
		CachedTokens:        800,
		CacheReadTokens:     800,
		CacheCreationTokens: 50,
		TotalTokens:         1540,
	}
	if got != want {
		t.Errorf("devinUsageToDetail() = %+v, want %+v", got, want)
	}
}

// TestDevinUsageToDetailEmpty pins that an empty upstream usage still produces
// a zero Detail rather than garbage, so EnsurePublished records the request
// even when devin omits token counts.
func TestDevinUsageToDetailEmpty(t *testing.T) {
	if got := devinUsageToDetail(devinUsage{}); got != (usage.Detail{}) {
		t.Errorf("devinUsageToDetail(empty) = %+v, want zero Detail", got)
	}
}

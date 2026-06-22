package helps

import (
	"strings"
	"testing"
)

func TestSelectClaudeCodeBetas_BaseMatchesReference(t *testing.T) {
	betas := splitClaudeCodeBetasForTest(SelectClaudeCodeBetas([]byte(`{}`), nil))

	assertBetaExactForTest(t, betas, []string{
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"advisor-tool-2026-03-01",
		"advanced-tool-use-2025-11-20",
		"extended-cache-ttl-2025-04-11",
		"cache-diagnosis-2026-04-07",
	})
}

func TestSelectClaudeCodeBetas_FullAgentMatchesReference(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "Bash"}],
		"system": [],
		"thinking": {},
		"context_management": {},
		"output_config": {},
		"diagnostics": {}
	}`)

	betas := splitClaudeCodeBetasForTest(SelectClaudeCodeBetas(body, nil))

	assertBetaExactForTest(t, betas, []string{
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"claude-code-20250219",
		"advisor-tool-2026-03-01",
		"advanced-tool-use-2025-11-20",
		"extended-cache-ttl-2025-04-11",
		"cache-diagnosis-2026-04-07",
	})
}

func TestSelectClaudeCodeBetas_StructuredOutputMatchesReference(t *testing.T) {
	body := []byte(`{
		"output_config": {"format": {"type": "json_schema"}}
	}`)

	betas := splitClaudeCodeBetasForTest(SelectClaudeCodeBetas(body, nil))

	assertBetaExactForTest(t, betas, []string{
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"advisor-tool-2026-03-01",
		"structured-outputs-2025-12-15",
		"cache-diagnosis-2026-04-07",
	})
}

func TestSelectClaudeCodeBetas_DeduplicatesExtraBetasAfterSelectedTier(t *testing.T) {
	betas := splitClaudeCodeBetasForTest(SelectClaudeCodeBetas([]byte(`{}`), []string{
		"oauth-2025-04-20",
		"custom-beta-2099-01-01",
	}))

	if countBetaForTest(betas, "oauth-2025-04-20") != 1 {
		t.Fatalf("expected oauth beta to be deduplicated, got %q", betas)
	}
	if betas[len(betas)-1] != "custom-beta-2099-01-01" {
		t.Fatalf("expected custom extra beta appended last, got %q", betas)
	}
}

func splitClaudeCodeBetasForTest(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func assertBetaExactForTest(t *testing.T, actual []string, expected []string) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("expected exact betas %q, got %q", expected, actual)
	}
	for i := range expected {
		if actual[i] != expected[i] {
			t.Fatalf("beta mismatch at %d: expected %q, got %q (full: %q)", i, expected[i], actual[i], actual)
		}
	}
}

func countBetaForTest(betas []string, target string) int {
	count := 0
	for _, beta := range betas {
		if beta == target {
			count++
		}
	}
	return count
}

package helps

import (
	"strings"
	"testing"
)

func TestSelectClaudeCodeBetas_BaseIncludesReferenceHeaderBetas(t *testing.T) {
	betas := splitClaudeCodeBetasForTest(SelectClaudeCodeBetas([]byte(`{}`), nil))

	assertBetaOrderForTest(t, betas, []string{
		"max-tokens-3-5-sonnet-2024-07-15",
		"output-128k-2025-02-19",
		"message-batches-2025-03-26",
		"oauth-2025-04-20",
	})
}

func TestSelectClaudeCodeBetas_FullAgentIncludesReferenceHeaderAndStreamingBetas(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "Bash"}],
		"system": [],
		"thinking": {},
		"context_management": {},
		"output_config": {},
		"diagnostics": {}
	}`)

	betas := splitClaudeCodeBetasForTest(SelectClaudeCodeBetas(body, nil))

	assertBetaOrderForTest(t, betas, []string{
		"max-tokens-3-5-sonnet-2024-07-15",
		"computer-use-2024-10-22",
		"computer-use-2025-01-24",
		"pdfs-2024-09-25",
		"token-efficient-tools-2025-02-19",
		"output-128k-2025-02-19",
		"message-batches-2025-03-26",
		"fine-grained-tool-streaming-2025-05-14",
		"output-8192-2025-02-19",
		"oauth-2025-04-20",
	})
}

func TestSelectClaudeCodeBetas_StructuredOutputIncludesReferenceHeaderBetas(t *testing.T) {
	body := []byte(`{
		"output_config": {"format": {"type": "json_schema"}}
	}`)

	betas := splitClaudeCodeBetasForTest(SelectClaudeCodeBetas(body, nil))

	assertBetaOrderForTest(t, betas, []string{
		"max-tokens-3-5-sonnet-2024-07-15",
		"computer-use-2024-10-22",
		"computer-use-2025-01-24",
		"pdfs-2024-09-25",
		"token-efficient-tools-2025-02-19",
		"output-128k-2025-02-19",
		"message-batches-2025-03-26",
		"fine-grained-tool-streaming-2025-05-14",
		"output-8192-2025-02-19",
		"structured-outputs-2025-12-15",
	})
}

func TestSelectClaudeCodeBetas_DeduplicatesExtraBetasAfterSelectedTier(t *testing.T) {
	betas := splitClaudeCodeBetasForTest(SelectClaudeCodeBetas([]byte(`{}`), []string{
		"output-128k-2025-02-19",
		"custom-beta-2099-01-01",
	}))

	if countBetaForTest(betas, "output-128k-2025-02-19") != 1 {
		t.Fatalf("expected output-128k beta to be deduplicated, got %q", betas)
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

func assertBetaOrderForTest(t *testing.T, actual []string, expectedOrdered []string) {
	t.Helper()
	next := 0
	for _, beta := range actual {
		if next < len(expectedOrdered) && beta == expectedOrdered[next] {
			next++
		}
	}
	if next != len(expectedOrdered) {
		t.Fatalf("expected ordered beta subsequence %q in %q", expectedOrdered, actual)
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

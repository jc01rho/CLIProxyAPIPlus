package helps

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Claude Code beta tiers — mirrors anthropic-auth's selectClaudeCodeBetas.
// Full-agent shape gets the most betas; structured-output gets a subset;
// everything else gets the base set.

var claudeCodeFullAgentBetas = []string{
	"claude-code-20250219",
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"advisor-tool-2026-03-01",
	"advanced-tool-use-2025-11-20",
	"context-1m-2025-08-07",
	"effort-2025-11-24",
	"extended-cache-ttl-2025-04-11",
	"cache-diagnosis-2026-04-07",
}

var claudeCodeStructuredOutputBetas = []string{
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"advisor-tool-2026-03-01",
	"structured-outputs-2025-12-15",
	"cache-diagnosis-2026-04-07",
}

var claudeCodeBaseBetas = []string{
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"advisor-tool-2026-03-01",
	"cache-diagnosis-2026-04-07",
}

// SelectClaudeCodeBetas returns the appropriate beta string based on body shape.
// body is the raw JSON request body; pass nil for non-body contexts.
func SelectClaudeCodeBetas(body []byte, extraBetas []string) string {
	var selected []string

	switch {
	case hasFullAgentShape(body):
		selected = claudeCodeFullAgentBetas
	case hasStructuredOutput(body):
		selected = claudeCodeStructuredOutputBetas
	default:
		selected = claudeCodeBaseBetas
	}

	// Fast mode: add fast-mode beta when body.speed === "fast"
	if isFastMode(body) {
		selected = append(selected, "fast-mode-2026-02-01")
	}

	// Deduplicate with extra betas
	seen := make(map[string]bool, len(selected)+len(extraBetas))
	result := make([]string, 0, len(selected)+len(extraBetas))
	for _, b := range selected {
		b = strings.TrimSpace(b)
		if b != "" && !seen[b] {
			result = append(result, b)
			seen[b] = true
		}
	}
	for _, b := range extraBetas {
		b = strings.TrimSpace(b)
		if b != "" && !seen[b] {
			result = append(result, b)
			seen[b] = true
		}
	}

	return strings.Join(result, ",")
}

// isFastMode checks if the body has speed === "fast".
func isFastMode(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	return gjson.GetBytes(body, "speed").String() == "fast"
}

// hasFullAgentShape checks if the body has the full Claude Code agent shape:
// tools, system, thinking, context_management, output_config, diagnostics.
func hasFullAgentShape(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	return hasField(body, "tools") &&
		hasField(body, "system") &&
		hasField(body, "thinking") &&
		hasField(body, "context_management") &&
		hasField(body, "output_config") &&
		hasField(body, "diagnostics")
}

// hasStructuredOutput checks if the body has output_config with json_schema format.
func hasStructuredOutput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	// Check for output_config.format.type == "json_schema"
	outputConfig := gjson.GetBytes(body, "output_config")
	if !outputConfig.Exists() {
		return false
	}
	format := gjson.GetBytes(body, "output_config.format")
	if !format.Exists() {
		return false
	}
	return gjson.GetBytes(body, "output_config.format.type").String() == "json_schema"
}

// hasField checks if a JSON body has a non-null field at the top level.
func hasField(body []byte, field string) bool {
	return gjson.GetBytes(body, field).Exists()
}

package helps

import (
	"strings"

	"github.com/tidwall/gjson"
)

// claudeCodeFullAgentBetas contains the full-set beta flags used for Claude Code
// full-agent tier requests. These match the anthropic-auth reference repository's
// configureClaudeCodeHeaders combined with selectClaudeCodeBetas.
var claudeCodeFullAgentBetas = []string{
	// From configureClaudeCodeHeaders (full-agent tier)
	"max-tokens-3-5-sonnet-2024-07-15",
	"computer-use-2024-10-22",
	"computer-use-2025-01-24",
	"pdfs-2024-09-25",
	"token-efficient-tools-2025-02-19",
	"output-128k-2025-02-19",
	"message-batches-2025-03-26",
	"fine-grained-tool-streaming-2025-05-14",
	"output-8192-2025-02-19",
	// Proxy-specific additional betas
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
}

// claudeCodeStructuredOutputBetas contains the beta flags for Claude Code
// structured-output tier requests.
var claudeCodeStructuredOutputBetas = []string{
	// From configureClaudeCodeHeaders (structured-output tier, extends full-agent)
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
	// Proxy-specific additional betas
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"advisor-tool-2026-03-01",
	"cache-diagnosis-2026-04-07",
}

// claudeCodeBaseBetas contains the minimal beta flags for Claude Code
// base-tier requests.
var claudeCodeBaseBetas = []string{
	// From configureClaudeCodeHeaders (base tier)
	"max-tokens-3-5-sonnet-2024-07-15",
	"output-128k-2025-02-19",
	"message-batches-2025-03-26",
	// Proxy-specific additional betas
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"advisor-tool-2026-03-01",
	"advanced-tool-use-2025-11-20",
	"extended-cache-ttl-2025-04-11",
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

// hasFullAgentShape checks if the body has the full Claude Code agent shape.
// Mirrors cortexkit/anthropic-auth hasFullAgentShape() exactly:
//   - tools is a non-empty array
//   - system is an array
//   - thinking, context_management, output_config, diagnostics are objects
func hasFullAgentShape(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	if !gjson.GetBytes(body, "system").IsArray() {
		return false
	}
	return isJSONObject(body, "thinking") &&
		isJSONObject(body, "context_management") &&
		isJSONObject(body, "output_config") &&
		isJSONObject(body, "diagnostics")
}

// isJSONObject reports whether the given top-level field is a JSON object.
func isJSONObject(body []byte, field string) bool {
	v := gjson.GetBytes(body, field)
	return v.Exists() && v.IsObject()
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

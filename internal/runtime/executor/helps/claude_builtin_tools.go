package helps

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

var claudeServerToolVersionPattern = regexp.MustCompile(`_\d{8}$`)

var defaultClaudeBuiltinToolNames = []string{
	"web_search",
	"web_fetch",
	"code_execution",
	"text_editor",
	"str_replace_editor",
	"str_replace_based_edit_tool",
	"computer",
	"bash",
	"advisor",
	"agent_toolset",
	"memory",
	"tool_search_tool_bm25",
	"tool_search_tool_regex",
}

func newClaudeBuiltinToolRegistry() map[string]bool {
	registry := make(map[string]bool, len(defaultClaudeBuiltinToolNames))
	for _, name := range defaultClaudeBuiltinToolNames {
		registry[name] = true
	}
	return registry
}

// IsClaudeServerToolType reports whether a typed declaration is a recognized
// Anthropic-operated tool. Client-defined type:"custom" declarations are not
// server tools and must remain eligible for MCP aliasing.
func IsClaudeServerToolType(toolType string) bool {
	toolType = strings.ToLower(strings.TrimSpace(toolType))
	for _, prefix := range []string{
		"advisor_",
		"agent_toolset_",
		"bash_",
		"code_execution_",
		"computer_",
		"memory_",
		"text_editor_",
		"tool_search_tool_",
		"web_fetch_",
		"web_search_",
	} {
		base := strings.TrimSuffix(prefix, "_")
		if toolType == base || strings.HasPrefix(toolType, prefix) {
			return true
		}
	}
	return false
}

// CanonicalClaudeServerToolName returns the canonical tool name required by
// Anthropic for a typed server tool (e.g. "tool_search_tool_bm25" for
// "tool_search_tool_bm25_20251119", "web_search" for "web_search_20250305").
// If the toolType is not a known Claude server/client tool type, it returns "".
func CanonicalClaudeServerToolName(toolType string) string {
	toolType = strings.ToLower(strings.TrimSpace(toolType))
	if !IsClaudeServerToolType(toolType) {
		return ""
	}
	switch {
	case strings.HasPrefix(toolType, "text_editor_20250728"):
		return "str_replace_based_edit_tool"
	case strings.HasPrefix(toolType, "text_editor_20250124"), strings.HasPrefix(toolType, "text_editor_20241022"):
		return "str_replace_editor"
	default:
		return claudeServerToolVersionPattern.ReplaceAllString(toolType, "")
	}
}

func AugmentClaudeBuiltinToolRegistry(body []byte, registry map[string]bool) map[string]bool {
	if registry == nil {
		registry = newClaudeBuiltinToolRegistry()
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return registry
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		toolType := tool.Get("type").String()
		if !IsClaudeServerToolType(toolType) {
			return true
		}
		if name := tool.Get("name").String(); name != "" {
			registry[name] = true
		}
		if canonical := CanonicalClaudeServerToolName(toolType); canonical != "" {
			registry[canonical] = true
		}
		return true
	})
	return registry
}

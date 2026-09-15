package helps

import (
	"regexp"
	"strings"
)

// Cursor tool-result text normalization (ported from opencodex/src/adapters/cursor/tool-result-normalize.ts).
//
// Scoped to the Computer Use / node_repl branch: empty-output and known-failure-state
// normalization ships here. The original TS file annotates code-mode host failures too,
// but CPA does not advertise code-mode itself — that responsibility stays on the upstream
// adapter. The package stays self-contained so it can be unit-tested without protobuf or
// any external dependency on exec-tool-result-normalize.ts.

const (
	cursorEmptyExecOutputMessage = "[empty output: the tool ran but produced no stdout or return value. Verify application state with get_app_state, or make the script emit output.]"
	cursorFailedExecOutputMessage = "[script failed: no error detail returned]"
)

var (
	cursorSuccessWrapperRegex = regexp.MustCompile(`^(?:Script completed|Command finished|Execution finished)\b`)
)

// cursorComputerUseToolNames enumerates bare tool names that belong to the Computer Use /
// node_repl runtime. Membership check uses case-insensitive comparison.
var cursorComputerUseToolNames = map[string]struct{}{
	"node_repl":          {},
	"node_repl__js":      {},
	"mcp__node_repl__js": {},
	"get_app_state":      {},
	"list_apps":          {},
	"screenshot":         {},
	"computer_use":       {},
}

// CursorRuntimeFailure pairs a marker substring with a one-line recovery hint. Matches
// opencodex RUNTIME_FAILURE_GUIDANCE.
type CursorRuntimeFailure struct {
	Marker   string
	Guidance string
}

var cursorRuntimeFailureGuidance = []CursorRuntimeFailure{
	{
		Marker:   "SkyComputerUseError",
		Guidance: "The Computer Use runtime rejected this action. Re-check application state with get_app_state before retrying.",
	},
	{
		Marker:   "sky is not defined",
		Guidance: "The sky binding is unavailable in this context; Computer Use calls only work inside the privileged node_repl session.",
	},
	{
		Marker:   "has already been declared",
		Guidance: "The node_repl session keeps earlier declarations; rename the variable or use var/reassignment instead of redeclaring.",
	},
	{
		Marker:   "unsupported import in exec",
		Guidance: "Imports are not available in this exec context; use the injected globals instead.",
	},
}

// CursorNormalizedToolResult is the post-normalization payload. `Changed` reports whether
// either field was rewritten so callers can skip downstream work on the common path.
type CursorNormalizedToolResult struct {
	Text    string
	IsError bool
	Changed bool
}

// CursorToolResultOptions controls normalization. Empty fields are interpreted as "not
// provided" rather than "must match exactly".
type CursorToolResultOptions struct {
	ToolName      string
	ToolNamespace string
	IsError       bool
}

// IsCursorComputerUseTool reports whether the tool name or namespace belongs to the
// Computer Use / node_repl runtime. Matches opencodex isNodeReplOrComputerUseTool.
func IsCursorComputerUseTool(toolName, toolNamespace string) bool {
	if toolNamespace != "" {
		lower := strings.ToLower(toolNamespace)
		if strings.Contains(lower, "node_repl") || strings.Contains(lower, "computer_use") {
			return true
		}
	}
	if toolName == "" {
		return false
	}
	lower := strings.ToLower(toolName)
	if _, ok := cursorComputerUseToolNames[lower]; ok {
		return true
	}
	return strings.HasPrefix(lower, "mcp__node_repl") || strings.HasPrefix(lower, "mcp__computer_use")
}

// IsCursorEmptyOrFailedExecOutput reports whether the trimmed text is empty or matches
// the "Script failed"-style wrapper that opencodex treats as a failure signal even though
// the upstream wrapper is non-error.
func IsCursorEmptyOrFailedExecOutput(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	return strings.Contains(strings.ToLower(trimmed), "script failed")
}

// NormalizeCursorToolResultText applies the Computer Use / node_repl normalization chain.
// Pass-through when nothing matches.
func NormalizeCursorToolResultText(text string, options CursorToolResultOptions) CursorNormalizedToolResult {
	isError := options.IsError
	computerUse := IsCursorComputerUseTool(options.ToolName, options.ToolNamespace)
	if computerUse && IsCursorEmptyOrFailedExecOutput(text) {
		// Always treat empty/failed-empty wrappers as errors on the Computer Use branch.
		return CursorNormalizedToolResult{Text: cursorEmptyExecOutputMessage, IsError: true, Changed: true}
	}
	// Replayed guidance and successful wrappers must not enter the runtime marker matcher.
	if cursorSuccessWrapperRegex.MatchString(strings.TrimSpace(text)) {
		return CursorNormalizedToolResult{Text: text, IsError: isError, Changed: false}
	}
	if computerUse && !isError {
		for _, fg := range cursorRuntimeFailureGuidance {
			if strings.Contains(text, fg.Marker) {
				return CursorNormalizedToolResult{
					Text:    text + "\n[recovery: " + fg.Guidance + "]",
					IsError: true,
					Changed: true,
				}
			}
		}
	}
	return CursorNormalizedToolResult{Text: text, IsError: isError, Changed: false}
}

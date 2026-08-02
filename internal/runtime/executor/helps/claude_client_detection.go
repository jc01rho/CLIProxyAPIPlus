package helps

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

var (
	claudeCodeUserAgentPattern        = regexp.MustCompile(`(?i)^claude-cli/`)
	claudeCodeUserAgentDetailsPattern = regexp.MustCompile(`(?i)^claude-cli/\S+\s+\(external,\s*([^,)]+)(?:,\s*agent-sdk/([^,)]+))?`)
)

var claudeCodeSubclientByEntrypoint = map[string]string{
	"cli":                       "claude-code-cli",
	"mcp":                       "claude-code-mcp",
	"bench":                     "claude-code-bench",
	"sdk-cli":                   "claude-code-cli-sdk",
	"sdk-ts":                    "claude-code-sdk-ts",
	"sdk-py":                    "claude-code-sdk-py",
	"claude-vscode":             "claude-code-vscode",
	"claude-code-github-action": "claude-code-gh-action",
	"local-agent":               "claude-local-agent",
	"local_agent":               "claude-local-agent",
	"claude-desktop":            "claude-desktop",
	"claude-desktop-3p":         "claude-desktop-3p",
	"remote":                    "claude-remote",
	"remote_baku":               "claude-remote-baku",
	"remote_cowork":             "claude-remote-cowork",
	"remote_trigger":            "claude-remote-trigger",
	"remote_desktop":            "claude-remote-desktop",
	"remote_mobile":             "claude-remote-mobile",
	"claude_in_slack":           "claude-in-slack",
	"claude-in-slack":           "claude-in-slack",
	"claude-in-teams":           "claude-in-teams",
	"claude-security":           "claude-security",
	"ssh-remote":                "claude-ssh-remote",
	"claude-coworker":           "claude-coworker",
	"claude-coworker-terminal":  "claude-coworker-terminal",
}

// Only product surfaces with verified 2.1.220 wire behavior are eligible for
// pass-through. Other first-party-looking entrypoints are cloaked until their
// CPA-reachable request shape has been captured and reviewed.
var nativeClaudeEntrypoints = map[string]bool{
	"cli":           true,
	"sdk-cli":       true,
	"claude-vscode": true,
}

// ClaudeCodeRequestDetection records the strong signals and first-party
// subclient identity used to distinguish an official Claude Code request from
// a client that only copied its User-Agent.
type ClaudeCodeRequestDetection struct {
	Confirmed       bool
	StrongSignals   bool
	NativeClient    bool
	XAppCLI         bool
	UserAgent       bool
	BetasPresent    bool
	MetadataUserID  bool
	Entrypoint      string
	Subclient       string
	AgentSDKVersion string
}

// DetectClaudeCodeRequest first mirrors CCH's strong-signal contract, then
// applies CPA's native-client policy. Messages requests require all four strong
// signals; count_tokens omits metadata.user_id and uses the three header signals.
// Only Anthropic first-party product entrypoints are confirmed for pass-through.
// Generic sdk-ts/sdk-py Agent SDK entrypoints remain unconfirmed and receive
// CLI cloaking; native Claude Code print mode keeps its original sdk-cli identity.
func DetectClaudeCodeRequest(headers http.Header, payload []byte, countTokens bool) ClaudeCodeRequestDetection {
	userAgent := headerValue(headers, "User-Agent")
	entrypoint, agentSDKVersion := parseClaudeCodeUserAgentDetails(userAgent)
	detection := ClaudeCodeRequestDetection{
		XAppCLI:         headerValue(headers, "X-App") == "cli",
		UserAgent:       claudeCodeUserAgentPattern.MatchString(userAgent),
		BetasPresent:    headerPresent(headers, "Anthropic-Beta"),
		Entrypoint:      entrypoint,
		Subclient:       claudeCodeSubclientByEntrypoint[entrypoint],
		AgentSDKVersion: agentSDKVersion,
	}

	metadataUserID := gjson.GetBytes(payload, "metadata.user_id")
	detection.MetadataUserID = metadataUserID.Exists() && metadataUserID.Type == gjson.String
	detection.StrongSignals = detection.XAppCLI && detection.UserAgent && detection.BetasPresent && (countTokens || detection.MetadataUserID)
	detection.NativeClient = nativeClaudeEntrypoints[entrypoint]
	detection.Confirmed = detection.StrongSignals && detection.NativeClient
	return detection
}

func parseClaudeCodeUserAgentDetails(userAgent string) (entrypoint, agentSDKVersion string) {
	matches := claudeCodeUserAgentDetailsPattern.FindStringSubmatch(strings.TrimSpace(userAgent))
	if len(matches) < 2 {
		return "", ""
	}
	entrypoint = strings.ToLower(strings.TrimSpace(matches[1]))
	if len(matches) >= 3 {
		agentSDKVersion = strings.TrimSpace(matches[2])
	}
	return entrypoint, agentSDKVersion
}

func headerValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) || len(values) == 0 {
			continue
		}
		return values[0]
	}
	return ""
}

func headerPresent(headers http.Header, name string) bool {
	if headers == nil {
		return false
	}
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

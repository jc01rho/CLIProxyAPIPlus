package executor

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// This file ports cortexkit/anthropic-auth system-prompt handling for the Claude
// OAuth masquerade so the /v1/messages wire body matches the reference exactly.
//
// Reference: packages/opencode/src/transform.ts (prependClaudeCodeIdentity,
// _sanitizeSystemText) and packages/core/src/constants.ts.

// claudeCodeIdentityText mirrors CLAUDE_CODE_IDENTITY in constants.ts.
const claudeCodeIdentityText = "You are Claude Code, Anthropic's official CLI for Claude."

// opencodeIdentityPrefix mirrors OPENCODE_IDENTITY_PREFIX in constants.ts.
const opencodeIdentityPrefix = "You are OpenCode"

// paragraphRemovalAnchors mirrors PARAGRAPH_REMOVAL_ANCHORS in constants.ts.
// Any paragraph (text between blank lines) containing one of these strings is
// removed entirely, resilient to upstream rewording.
var paragraphRemovalAnchors = []string{
	"github.com/anomalyco/opencode",
	"opencode.ai/docs",
}

// textReplacement mirrors a TEXT_REPLACEMENTS entry in constants.ts.
type textReplacement struct {
	match       string
	replacement string
}

// claudeCodeTextReplacements mirrors TEXT_REPLACEMENTS in constants.ts. These
// handle inline "OpenCode" occurrences inside paragraphs we want to keep.
var claudeCodeTextReplacements = []textReplacement{
	{match: "if OpenCode honestly", replacement: "if the assistant honestly"},
	{
		match:       "Here is some useful information about the environment you are running in:",
		replacement: "Environment context you are running in:",
	},
}

// splitClaudeSystemParagraphs splits text into paragraphs on one or more blank
// lines, mirroring JS text.split(/\n\n+/).
func splitClaudeSystemParagraphs(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	var paragraphs []string
	var current strings.Builder
	newlineRun := 0
	for _, r := range normalized {
		if r == '\n' {
			newlineRun++
			continue
		}
		if newlineRun >= 2 {
			paragraphs = append(paragraphs, current.String())
			current.Reset()
		} else if newlineRun == 1 {
			current.WriteByte('\n')
		}
		newlineRun = 0
		current.WriteRune(r)
	}
	paragraphs = append(paragraphs, current.String())
	return paragraphs
}

// sanitizeClaudeSystemText removes OpenCode branding from a system-prompt text
// block, mirroring cortexkit _sanitizeSystemText():
//  1. Drop paragraphs containing OPENCODE_IDENTITY_PREFIX.
//  2. Drop paragraphs containing any PARAGRAPH_REMOVAL_ANCHORS string.
//  3. Apply TEXT_REPLACEMENTS inline.
func sanitizeClaudeSystemText(text string) string {
	paragraphs := splitClaudeSystemParagraphs(text)
	filtered := make([]string, 0, len(paragraphs))
	for _, p := range paragraphs {
		if strings.Contains(p, opencodeIdentityPrefix) {
			continue
		}
		dropped := false
		for _, anchor := range paragraphRemovalAnchors {
			if strings.Contains(p, anchor) {
				dropped = true
				break
			}
		}
		if dropped {
			continue
		}
		filtered = append(filtered, p)
	}

	result := strings.Join(filtered, "\n\n")
	for _, rule := range claudeCodeTextReplacements {
		result = strings.Replace(result, rule.match, rule.replacement, 1)
	}
	return strings.TrimSpace(result)
}

// claudeJSONString encodes a Go string as a JSON string literal.
func claudeJSONString(s string) string {
	encoded, err := sjson.Set("{}", "v", s)
	if err != nil {
		return `""`
	}
	return gjson.Get(encoded, "v").Raw
}

// prependClaudeCodeIdentityToSystem rewrites the request body's system to match
// cortexkit prependClaudeCodeIdentity(): sanitize each client system text block and
// prepend a single Claude Code identity block. Handles undefined/string/object/array
// system shapes.
func prependClaudeCodeIdentityToSystem(body []byte) []byte {
	identityBlock := `{"type":"text","text":` + claudeJSONString(claudeCodeIdentityText) + `}`

	system := gjson.GetBytes(body, "system")

	var blocks []string
	switch {
	case !system.Exists():
		blocks = []string{identityBlock}
	case system.Type == gjson.String:
		sanitized := sanitizeClaudeSystemText(system.String())
		if sanitized == claudeCodeIdentityText {
			blocks = []string{identityBlock}
		} else {
			blocks = []string{identityBlock, `{"type":"text","text":` + claudeJSONString(sanitized) + `}`}
		}
	case system.IsArray():
		arr := system.Array()
		sanitized := make([]string, 0, len(arr)+1)
		for _, item := range arr {
			if item.Type == gjson.String {
				sanitized = append(sanitized, `{"type":"text","text":`+claudeJSONString(sanitizeClaudeSystemText(item.String()))+`}`)
				continue
			}
			if item.IsObject() && item.Get("type").String() == "text" && item.Get("text").Type == gjson.String {
				updated, err := sjson.Set(item.Raw, "text", sanitizeClaudeSystemText(item.Get("text").String()))
				if err == nil {
					sanitized = append(sanitized, updated)
					continue
				}
			}
			sanitized = append(sanitized, `{"type":"text","text":`+claudeJSONString(item.String())+`}`)
		}
		if len(sanitized) > 0 && gjson.Get(sanitized[0], "text").String() == claudeCodeIdentityText {
			blocks = sanitized
		} else {
			blocks = append([]string{identityBlock}, sanitized...)
		}
	case system.IsObject():
		typ := system.Get("type").String()
		if typ == "" {
			typ = "text"
		}
		txt := ""
		if system.Get("text").Type == gjson.String {
			txt = system.Get("text").String()
		}
		objBlock, err := sjson.Set(system.Raw, "text", sanitizeClaudeSystemText(txt))
		if err == nil {
			objBlock, _ = sjson.Set(objBlock, "type", typ)
			blocks = []string{identityBlock, objBlock}
		} else {
			blocks = []string{identityBlock}
		}
	default:
		blocks = []string{identityBlock}
	}

	out, err := sjson.SetRawBytes(body, "system", []byte("["+strings.Join(blocks, ",")+"]"))
	if err != nil {
		return body
	}
	return out
}

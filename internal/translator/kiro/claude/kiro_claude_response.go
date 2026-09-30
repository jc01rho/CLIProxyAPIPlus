// Package claude provides response translation functionality for Kiro API to Claude format.
// This package handles the conversion of Kiro API responses into Claude-compatible format,
// including support for thinking blocks and tool use.
package claude

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"

	kirocommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/kiro/common"
)

// generateThinkingSignature is a legacy compatibility fallback ONLY for blocks
// with no upstream signature. This content hash is not a Kiro attestation and
// must never replace a real signature.
func generateThinkingSignature(thinkingContent string) string {
	if thinkingContent == "" {
		return ""
	}
	// Generate a deterministic signature based on content hash
	hash := sha256.Sum256([]byte(thinkingContent))
	return base64.StdEncoding.EncodeToString(hash[:])
}

// BuildClaudeUsage preserves aggregate input/output counts and exposes cache
// counters without subtracting or adding them to the aggregate totals.
// Ported from kiro-lb src/stream_anthropic.rs (1581af9).
func BuildClaudeUsage(usageInfo usage.Detail) map[string]interface{} {
	result := map[string]interface{}{
		"input_tokens":  usageInfo.InputTokens,
		"output_tokens": usageInfo.OutputTokens,
	}
	result["cache_read_input_tokens"] = usageInfo.CacheReadTokens
	result["cache_creation_input_tokens"] = usageInfo.CacheCreationTokens
	return result
}

var (
	thinkingStartTag = kirocommon.ThinkingStartTag
	thinkingEndTag   = kirocommon.ThinkingEndTag
)

// BuildClaudeResponse constructs a Claude-compatible response. It uses a
// deterministic hash only when no Kiro signature was supplied.
func BuildClaudeResponse(content string, toolUses []KiroToolUse, model string, usageInfo usage.Detail, stopReason string) []byte {
	return BuildClaudeResponseWithThinkingSignatures(content, nil, toolUses, model, usageInfo, stopReason)
}

// BuildClaudeResponseWithThinkingSignatures attaches Kiro's opaque signatures
// to thinking blocks in order. Supply one entry per thinking block, using ""
// for unsigned blocks. Only unsigned blocks use the legacy hash fallback.
// Ported from kiro-lb src/stream_anthropic.rs (1581af9).
func BuildClaudeResponseWithThinkingSignatures(content string, thinkingSignatures []string, toolUses []KiroToolUse, model string, usageInfo usage.Detail, stopReason string) []byte {
	var contentBlocks []map[string]interface{}

	// Extract thinking blocks and text from content.
	if content != "" {
		blocks := ExtractThinkingFromContentWithSignatures(content, thinkingSignatures)
		contentBlocks = append(contentBlocks, blocks...)

		// Log if thinking blocks were extracted
		for _, block := range blocks {
			if block["type"] == "thinking" {
				thinkingContent := block["thinking"].(string)
				log.Infof("kiro: buildClaudeResponse extracted thinking block (len: %d)", len(thinkingContent))
			}
		}
	}

	// Add tool_use blocks - skip truncated tools and log warning
	for _, toolUse := range toolUses {
		if toolUse.IsTruncated && toolUse.TruncationInfo != nil {
			log.Warnf("kiro: buildClaudeResponse skipping truncated tool: %s (ID: %s)", toolUse.Name, toolUse.ToolUseID)
			continue
		}
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type":  "tool_use",
			"id":    toolUse.ToolUseID,
			"name":  toolUse.Name,
			"input": toolUse.Input,
		})
	}

	// Ensure at least one content block (Claude API requires non-empty content)
	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}

	// Use upstream stopReason; apply fallback logic if not provided
	// SOFT_LIMIT_REACHED: Keep stop_reason = "tool_use" so Claude continues the loop
	if stopReason == "" {
		stopReason = "end_turn"
		if len(toolUses) > 0 {
			stopReason = "tool_use"
		}
		log.Debugf("kiro: buildClaudeResponse using fallback stop_reason: %s", stopReason)
	}

	// Log warning if response was truncated due to max_tokens
	if stopReason == "max_tokens" {
		log.Warnf("kiro: response truncated due to max_tokens limit (buildClaudeResponse)")
	}

	response := map[string]interface{}{
		"id":          "msg_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24],
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     contentBlocks,
		"stop_reason": stopReason,
		"usage":       BuildClaudeUsage(usageInfo),
	}
	result, _ := json.Marshal(response)
	return result
}

// ExtractThinkingFromContent parses thinking tags using the documented legacy
// hash fallback when Kiro did not provide an opaque signature.
func ExtractThinkingFromContent(content string) []map[string]interface{} {
	return ExtractThinkingFromContentWithSignatures(content, nil)
}

// ExtractThinkingFromContentWithSignatures parses tags and assigns signatures
// by thinking-block order (not text-block index). Missing/empty signatures use
// the legacy hash fallback; a supplied signature is preserved byte-for-byte.
func ExtractThinkingFromContentWithSignatures(content string, signatures []string) []map[string]interface{} {
	var blocks []map[string]interface{}
	thinkingIndex := 0
	signatureForBlock := func(thinking string) string {
		index := thinkingIndex
		thinkingIndex++
		if index < len(signatures) && signatures[index] != "" {
			return signatures[index]
		}
		return generateThinkingSignature(thinking)
	}

	if content == "" {
		return blocks
	}

	// Check if content contains thinking tags at all
	if !strings.Contains(content, thinkingStartTag) {
		// No thinking tags, return as plain text
		return []map[string]interface{}{
			{
				"type": "text",
				"text": content,
			},
		}
	}

	log.Debugf("kiro: extractThinkingFromContent - found thinking tags in content (len: %d)", len(content))

	remaining := content

	for len(remaining) > 0 {
		// Look for <thinking> tag
		startIdx := strings.Index(remaining, thinkingStartTag)

		if startIdx == -1 {
			// No more thinking tags, add remaining as text
			if strings.TrimSpace(remaining) != "" {
				blocks = append(blocks, map[string]interface{}{
					"type": "text",
					"text": remaining,
				})
			}
			break
		}

		// Add text before thinking tag (if any meaningful content)
		if startIdx > 0 {
			textBefore := remaining[:startIdx]
			if strings.TrimSpace(textBefore) != "" {
				blocks = append(blocks, map[string]interface{}{
					"type": "text",
					"text": textBefore,
				})
			}
		}

		// Move past the opening tag
		remaining = remaining[startIdx+len(thinkingStartTag):]

		// Find closing tag
		endIdx := strings.Index(remaining, thinkingEndTag)

		if endIdx == -1 {
			// No closing tag found, treat rest as thinking content (incomplete response)
			if strings.TrimSpace(remaining) != "" {
				blocks = append(blocks, map[string]interface{}{
					"type":      "thinking",
					"thinking":  remaining,
					"signature": signatureForBlock(remaining),
				})
				log.Warnf("kiro: extractThinkingFromContent - missing closing </thinking> tag")
			}
			break
		}

		// Extract thinking content between tags
		thinkContent := remaining[:endIdx]
		if strings.TrimSpace(thinkContent) != "" {
			blocks = append(blocks, map[string]interface{}{
				"type":      "thinking",
				"thinking":  thinkContent,
				"signature": signatureForBlock(thinkContent),
			})
			log.Debugf("kiro: extractThinkingFromContent - extracted thinking block (len: %d)", len(thinkContent))
		}

		// Move past the closing tag
		remaining = remaining[endIdx+len(thinkingEndTag):]
	}

	// If no blocks were created (all whitespace), return empty text block
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}

	return blocks
}

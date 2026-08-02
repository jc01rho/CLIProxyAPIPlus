package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

func resolveIncomingClaudeHeaders(ctx context.Context, incoming http.Header) http.Header {
	resolved := make(http.Header)
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		resolved = ginCtx.Request.Header.Clone()
	}
	for key, values := range incoming {
		resolved[key] = append([]string(nil), values...)
	}
	return resolved
}

func detectIncomingClaudeCodeRequest(ctx context.Context, incoming http.Header, payload []byte, countTokens bool) (http.Header, helps.ClaudeCodeRequestDetection) {
	resolved := resolveIncomingClaudeHeaders(ctx, incoming)
	return resolved, helps.DetectClaudeCodeRequest(resolved, payload, countTokens)
}

// getWorkloadFromContext extracts workload identifier from the gin request headers.
func getWorkloadFromContext(ctx context.Context) string {
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return strings.TrimSpace(ginCtx.GetHeader("X-CPA-Claude-Workload"))
	}
	return ""
}

// getCloakConfigFromAuth extracts cloak configuration from the auth's attributes,
// falling back to its stored metadata (the raw OAuth/token JSON). Returns
// (cloakMode, strictMode, sensitiveWords, cacheUserID); an empty cloakMode means
// the credential did not explicitly configure a mode.
func getCloakConfigFromAuth(auth *cliproxyauth.Auth) (cloakMode string, strictMode bool, sensitiveWords []string, cacheUserID bool) {
	if auth == nil {
		return "", false, nil, false
	}

	// lookupCloakAttr prefers the executor-facing Attributes, then falls back to the
	// raw metadata blob (e.g. the OAuth/token JSON) so file-based credentials can
	// carry cloak settings without a matching claude-api-key config entry.
	lookupCloakAttr := func(key string) string {
		if auth.Attributes != nil {
			if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
				return value
			}
		}
		if auth.Metadata != nil {
			if value, ok := auth.Metadata[key].(string); ok {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}

	// An empty cloakMode means this credential did not explicitly configure a mode,
	// allowing the caller to fall back to the global/default behavior.
	cloakMode = lookupCloakAttr("cloak_mode")

	strictMode = strings.EqualFold(lookupCloakAttr("cloak_strict_mode"), "true")

	if wordsStr := lookupCloakAttr("cloak_sensitive_words"); wordsStr != "" {
		sensitiveWords = strings.Split(wordsStr, ",")
		for i := range sensitiveWords {
			sensitiveWords[i] = strings.TrimSpace(sensitiveWords[i])
		}
	}

	cacheUserID = strings.EqualFold(lookupCloakAttr("cloak_cache_user_id"), "true")

	return cloakMode, strictMode, sensitiveWords, cacheUserID
}

// injectFakeUserID generates and injects a fake user ID into the request metadata.
// When useCache is false, a new user ID is generated for every call.
func injectFakeUserID(ctx context.Context, payload []byte, apiKey string, useCache bool) ([]byte, error) {
	generateID := func() (string, error) {
		if useCache {
			return helps.CachedUserIDRequired(ctx, apiKey)
		}
		sessionID, errSessionID := helps.CachedSessionIDRequired(ctx, apiKey)
		if errSessionID != nil {
			return "", errSessionID
		}
		return helps.GenerateFakeUserIDWithSessionID(sessionID), nil
	}

	metadata := gjson.GetBytes(payload, "metadata")
	if !metadata.Exists() {
		userID, errUserID := generateID()
		if errUserID != nil {
			return nil, errUserID
		}
		payload, _ = sjson.SetBytes(payload, "metadata.user_id", userID)
		return payload, nil
	}

	existingUserID := gjson.GetBytes(payload, "metadata.user_id").String()
	if existingUserID == "" || !helps.IsValidUserID(existingUserID) {
		userID, errUserID := generateID()
		if errUserID != nil {
			return nil, errUserID
		}
		payload, _ = sjson.SetBytes(payload, "metadata.user_id", userID)
	}
	return payload, nil
}

// fingerprintSalt is the salt used by Claude Code to compute the 3-char build fingerprint.
const fingerprintSalt = "59cf53e54c78"

// computeFingerprint computes the 3-char build fingerprint that Claude Code embeds in cc_version.
// Algorithm: SHA256(salt + messageText[4] + messageText[7] + messageText[20] + version)[:3]
func computeFingerprint(messageText, version string) string {
	indices := [3]int{4, 7, 20}
	runes := []rune(messageText)
	var sb strings.Builder
	for _, idx := range indices {
		if idx < len(runes) {
			sb.WriteRune(runes[idx])
		} else {
			sb.WriteRune('0')
		}
	}
	input := fingerprintSalt + sb.String() + version
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:3]
}

// generateBillingHeader creates the x-anthropic-billing-header text block that
// Claude Code prepends to its system prompt. cch is present only on signed paths.
func generateBillingHeader(cchSigning bool, version, messageText, entrypoint, workload string) string {
	if entrypoint == "" {
		entrypoint = "cli"
	}
	buildHash := computeFingerprint(messageText, version)
	workloadPart := ""
	if workload != "" {
		workloadPart = fmt.Sprintf(" cc_workload=%s;", workload)
	}

	if cchSigning {
		return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=00000;%s", version, buildHash, entrypoint, workloadPart)
	}
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s;%s", version, buildHash, entrypoint, workloadPart)
}

func claudeBillingFingerprintMessageText(payload []byte) string {
	messageText := ""
	gjson.GetBytes(payload, "messages").ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() != "user" {
			return true
		}
		content := message.Get("content")
		candidate := ""
		if content.Type == gjson.String {
			candidate = content.String()
		} else if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "text" {
					candidate = part.Get("text").String()
				}
				return true
			})
		}
		if candidate != "" {
			messageText = candidate
		}
		return true
	})
	return messageText
}

func claudeCCHFallbackBillingHeader(ctx context.Context, cfg *config.Config, payload []byte, entrypoint string) string {
	return generateBillingHeader(
		true,
		helps.DefaultClaudeVersion(cfg),
		claudeBillingFingerprintMessageText(payload),
		entrypoint,
		getWorkloadFromContext(ctx),
	)
}

const claudeCodeCLIIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

func checkSystemInstructionsWithMode(payload []byte, strictMode bool) []byte {
	return checkSystemInstructionsWithSigningMode(payload, strictMode, false, false, "2.1.220", "cli", "")
}

// checkSystemInstructionsWithSigningMode injects the two system blocks emitted
// by Claude Code 2.1.220 in --system-prompt "" CLI mode, moves any
// client-supplied system instructions into the first user message, and then
// prepends Claude Code's currentDate reminder.
func checkSystemInstructionsWithSigningMode(payload []byte, strictMode bool, cchSigning bool, oauthMode bool, version, entrypoint, workload string) []byte {
	system := gjson.GetBytes(payload, "system")
	messageText := claudeBillingFingerprintMessageText(payload)

	billingText := generateBillingHeader(cchSigning, version, messageText, entrypoint, workload)
	billingBlock := buildTextBlock(billingText, nil)
	agentBlock := buildTextBlock(claudeCodeCLIIdentity, map[string]string{"type": "ephemeral"})

	systemResult := "[" + billingBlock + "," + agentBlock + "]"
	payload, _ = sjson.SetRawBytes(payload, "system", []byte(systemResult))

	// Collect user system instructions and prepend to first user message.
	if !strictMode {
		var userSystemParts []string
		if system.IsArray() {
			system.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "text" {
					txt := strings.TrimSpace(part.Get("text").String())
					if txt != "" && !util.IsClaudeCodeAttributionSystemText(txt) {
						userSystemParts = append(userSystemParts, txt)
					}
				}
				return true
			})
		} else if system.Type == gjson.String && strings.TrimSpace(system.String()) != "" && !util.IsClaudeCodeAttributionSystemText(system.String()) {
			userSystemParts = append(userSystemParts, strings.TrimSpace(system.String()))
		}

		if len(userSystemParts) > 0 {
			combined := strings.Join(userSystemParts, "\n\n")
			if oauthMode {
				combined = sanitizeForwardedSystemPrompt(combined)
			}
			if strings.TrimSpace(combined) != "" {
				payload = prependToFirstUserMessage(payload, combined)
			}
		}
	}

	return injectClaudeCodeCurrentDate(payload, time.Now())
}

// sanitizeForwardedSystemPrompt reduces forwarded third-party system context to a
// tiny neutral reminder for Claude OAuth cloaking. The goal is to preserve only
// the minimum tool/task guidance while removing virtually all client-specific
// prompt structure that Anthropic may classify as third-party agent traffic.
func sanitizeForwardedSystemPrompt(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return strings.TrimSpace(`Use the available tools when needed to help with software engineering tasks.
Keep responses concise and focused on the user's request.
Prefer acting on the user's task over describing product-specific workflows.`)
}

// buildTextBlock constructs a JSON text block with JSON.stringify-compatible
// HTML characters. encoding/json's default \u003c escaping would change the
// exact currentDate bytes and therefore the final CCH.
func buildTextBlock(text string, cacheControl map[string]string) string {
	block := `{"type":"text","text":` + marshalJSONStringWithoutHTMLEscape(text)
	if cacheControl != nil && len(cacheControl) > 0 {
		block += `,"cache_control":{"type":"ephemeral"`
		if ttl, ok := cacheControl["ttl"]; ok {
			block += `,"ttl":` + marshalJSONStringWithoutHTMLEscape(ttl)
		}
		block += "}"
	}
	return block + "}"
}

func marshalJSONStringWithoutHTMLEscape(value string) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(encoded.String(), "\n")
}

// prependToFirstUserMessage injects text content into the first user message.
// This avoids putting non-Claude-Code system instructions in system[] which
// triggers Anthropic's extra usage billing for OAuth-proxied requests.
func prependToFirstUserMessage(payload []byte, text string) []byte {
	firstUserIdx := firstClaudeUserMessageIndex(payload)
	if firstUserIdx < 0 {
		return payload
	}

	prefixText := fmt.Sprintf(`<system-reminder>
As you answer the user's questions, you can use the following context from the system:
%s

IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>
`, text)
	prefixBlock := buildTextBlock(prefixText, nil)

	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := gjson.GetBytes(payload, contentPath)

	if content.IsArray() {
		var newArray string
		switch {
		case content.Raw == "[]" || content.Raw == "":
			newArray = "[" + prefixBlock + "]"
		case leadsWithToolResult(content):
			// Anthropic requires the user message that immediately follows an
			// assistant tool_use turn to lead with its tool_result blocks.
			// Append the reminder so those blocks stay at the head.
			if trimmed := strings.TrimRight(content.Raw, " \t\r\n"); strings.HasSuffix(trimmed, "]") {
				newArray = trimmed[:len(trimmed)-1] + "," + prefixBlock + "]"
			} else {
				newArray = "[" + prefixBlock + "," + content.Raw[1:]
			}
		default:
			newArray = "[" + prefixBlock + "," + content.Raw[1:]
		}
		payload, _ = sjson.SetRawBytes(payload, contentPath, []byte(newArray))
	} else if content.Type == gjson.String {
		userBlock := buildTextBlock(content.String(), nil)
		newArray := "[" + prefixBlock + "," + userBlock + "]"
		payload, _ = sjson.SetRawBytes(payload, contentPath, []byte(newArray))
	}

	return payload
}

// claudeCodeLocalDate reproduces Claude Code 2.1.220's wcs() helper:
// new Date(), local calendar fields, and zero-padded YYYY-MM-DD components.
func claudeCodeLocalDate(now time.Time) string {
	year, month, day := now.Date()
	return fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)
}

func claudeCodeCurrentDateReminder(now time.Time) string {
	return fmt.Sprintf(`<system-reminder>
As you answer the user's questions, you can use the following context:
# currentDate
Today's date is %s.

      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>

`, claudeCodeLocalDate(now))
}

func firstClaudeUserMessageIndex(payload []byte) int {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return -1
	}

	firstUserIdx := -1
	messages.ForEach(func(idx, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			firstUserIdx = int(idx.Int())
			return false
		}
		return true
	})
	return firstUserIdx
}

func isClaudeCodeContextReminder(text string) bool {
	return strings.HasPrefix(text, "<system-reminder>\nAs you answer the user's questions, you can use the following context:") ||
		strings.HasPrefix(text, "<system-reminder>\nAs you answer the user's questions, you can use the following context from the system:")
}

func injectClaudeCodeCurrentDate(payload []byte, now time.Time) []byte {
	firstUserIdx := firstClaudeUserMessageIndex(payload)
	if firstUserIdx < 0 {
		return payload
	}

	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := gjson.GetBytes(payload, contentPath)
	dateText := claudeCodeCurrentDateReminder(now)
	dateBlock := buildTextBlock(dateText, nil)

	if content.Type == gjson.String {
		userBlock := buildTextBlock(content.String(), map[string]string{"type": "ephemeral"})
		newArray := "[" + dateBlock + "," + userBlock + "]"
		payload, _ = sjson.SetRawBytes(payload, contentPath, []byte(newArray))
		return payload
	}
	if !content.IsArray() {
		return payload
	}

	dateAlreadyPresent := false
	actualTextIndex := -1
	content.ForEach(func(idx, block gjson.Result) bool {
		if block.Get("type").String() != "text" {
			return true
		}
		text := block.Get("text").String()
		if strings.Contains(text, "# currentDate\nToday's date is ") && isClaudeCodeContextReminder(text) {
			if int(idx.Int()) == 0 {
				dateAlreadyPresent = true
			}
			return true
		}
		if actualTextIndex < 0 && !isClaudeCodeContextReminder(text) {
			actualTextIndex = int(idx.Int())
		}
		return true
	})

	if actualTextIndex >= 0 {
		cachePath := fmt.Sprintf("%s.%d.cache_control", contentPath, actualTextIndex)
		payload, _ = sjson.SetRawBytes(payload, cachePath, []byte(`{"type":"ephemeral"}`))
		content = gjson.GetBytes(payload, contentPath)
	}

	if dateAlreadyPresent {
		payload, _ = sjson.SetRawBytes(payload, contentPath+".0.text", []byte(marshalJSONStringWithoutHTMLEscape(dateText)))
		payload, _ = sjson.DeleteBytes(payload, contentPath+".0.cache_control")
		return payload
	}

	var newArray string
	switch {
	case content.Raw == "[]" || content.Raw == "":
		newArray = "[" + dateBlock + "]"
	case leadsWithToolResult(content):
		// Keep tool_result at the head to satisfy Anthropic's request schema.
		if trimmed := strings.TrimRight(content.Raw, " \t\r\n"); strings.HasSuffix(trimmed, "]") {
			newArray = trimmed[:len(trimmed)-1] + "," + dateBlock + "]"
		} else {
			newArray = "[" + dateBlock + "," + content.Raw[1:]
		}
	default:
		newArray = "[" + dateBlock + "," + content.Raw[1:]
	}
	payload, _ = sjson.SetRawBytes(payload, contentPath, []byte(newArray))
	return payload
}

// leadsWithToolResult reports whether a message content array starts with a
// tool_result block. Such a message answers a preceding assistant tool_use turn,
// and Anthropic requires its tool_result blocks to remain first.
func leadsWithToolResult(content gjson.Result) bool {
	first := content.Get("0")
	return first.Exists() && first.Get("type").String() == "tool_result"
}

type claudeWirePolicy struct {
	OAuth               bool
	ConfirmedClaudeCode bool
	Cloak               bool
}

type claudeCloakSettings struct {
	strictMode     bool
	sensitiveWords []string
	cacheUserID    bool
}

func resolveClaudeWirePolicy(cfg *config.Config, auth *cliproxyauth.Auth, apiKey string, confirmedClaudeCode bool) (claudeWirePolicy, claudeCloakSettings) {
	cloakCfg := resolveClaudeKeyCloakConfig(cfg, auth)
	attrMode, attrStrict, attrWords, attrCache := getCloakConfigFromAuth(auth)

	cloakMode := "auto"
	if cfg != nil && cfg.DisableClaudeCloakMode {
		cloakMode = "never"
	}
	settings := claudeCloakSettings{
		strictMode:     attrStrict,
		sensitiveWords: attrWords,
		cacheUserID:    attrCache,
	}
	if attrMode != "" {
		cloakMode = attrMode
	}
	if cloakCfg != nil {
		if mode := strings.TrimSpace(cloakCfg.Mode); mode != "" {
			cloakMode = mode
		}
		if cloakCfg.StrictMode {
			settings.strictMode = true
		}
		if len(cloakCfg.SensitiveWords) > 0 {
			settings.sensitiveWords = cloakCfg.SensitiveWords
		}
		if cloakCfg.CacheUserID != nil {
			settings.cacheUserID = *cloakCfg.CacheUserID
		}
	}

	policy := claudeWirePolicy{
		OAuth:               isClaudeOAuthToken(apiKey),
		ConfirmedClaudeCode: confirmedClaudeCode,
		Cloak:               !confirmedClaudeCode,
	}
	if confirmedClaudeCode {
		// Native Claude Code is always a passthrough client. An operator-level
		// "always" mode may cloak unknown callers, but must not overwrite a
		// strongly confirmed CLI, sdk-cli, or claude-vscode fingerprint.
		policy.Cloak = false
		return policy, settings
	}
	switch strings.ToLower(strings.TrimSpace(cloakMode)) {
	case "always":
		policy.Cloak = true
	case "never":
		policy.Cloak = false
	}
	return policy, settings
}

// applyCloaking applies the shared Messages/count_tokens wire policy. The
// returned boolean reports whether cloaking ran.
func applyCloaking(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	payload []byte,
	apiKey string,
	confirmedClaudeCode bool,
	cchSigning bool,
) ([]byte, bool, error) {
	policy, settings := resolveClaudeWirePolicy(cfg, auth, apiKey, confirmedClaudeCode)
	if !policy.Cloak {
		return payload, false, nil
	}

	billingVersion := helps.DefaultClaudeVersion(cfg)
	workload := getWorkloadFromContext(ctx)
	payload = checkSystemInstructionsWithSigningMode(payload, settings.strictMode, cchSigning, policy.OAuth, billingVersion, "cli", workload)

	// OAuth metadata is rewritten after credential selection and all remaining
	// body mutations. Non-OAuth cloaking keeps the legacy generated identity.
	if !policy.OAuth {
		var errFakeUserID error
		payload, errFakeUserID = injectFakeUserID(ctx, payload, apiKey, settings.cacheUserID)
		if errFakeUserID != nil {
			return nil, false, errFakeUserID
		}
	}

	// Apply sensitive word obfuscation
	if len(settings.sensitiveWords) > 0 {
		matcher := helps.BuildSensitiveWordMatcher(settings.sensitiveWords)
		payload = helps.ObfuscateSensitiveWords(payload, matcher)
	}

	return payload, true, nil
}

// ensureCacheControl injects cache_control breakpoints into the payload for optimal prompt caching.
// According to Anthropic's documentation, cache prefixes are created in order: tools -> system -> messages.
// This function adds cache_control to:
// 1. The LAST non-deferred tool in the tools array (caches all preceding tool definitions)
// 2. The LAST system prompt element
// 3. The SECOND-TO-LAST user turn (caches conversation history for multi-turn)
//
// Up to 4 cache breakpoints are allowed per request. Tools, System, and Messages are INDEPENDENT breakpoints.
// This enables up to 90% cost reduction on cached tokens (cache read = 0.1x base price).
// See: https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
func ensureCacheControl(payload []byte) []byte {
	// 1. Inject cache_control into the LAST non-deferred tool
	// Tools are cached first in the hierarchy, so this is the most important breakpoint.
	payload = injectToolsCacheControl(payload)

	// 2. Inject cache_control into the LAST system prompt element
	// System is the second level in the cache hierarchy.
	payload = injectSystemCacheControl(payload)

	// 3. Inject cache_control into messages for multi-turn conversation caching
	// This caches the conversation history up to the second-to-last user turn.
	payload = injectMessagesCacheControl(payload)

	return payload
}

func countCacheControls(payload []byte) int {
	count := 0

	// Check system
	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
	}

	// Check tools
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
	}

	// Check messages
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					if item.Get("cache_control").Exists() {
						count++
					}
					return true
				})
			}
			return true
		})
	}

	return count
}

// normalizeCacheControlTTL ensures cache_control TTL values don't violate the
// prompt-caching-scope-2026-01-05 ordering constraint: a 1h-TTL block must not
// appear after a 5m-TTL block anywhere in the evaluation order.
//
// Anthropic evaluates blocks in order: tools → system (index 0..N) → messages.
// Within each section, blocks are evaluated in array order. A 5m (default) block
// followed by a 1h block at ANY later position is an error — including within
// the same section (e.g. system[1]=5m then system[3]=1h).
//
// Strategy: walk all cache_control blocks in evaluation order. Once a 5m block
// is seen, strip ttl from ALL subsequent 1h blocks (downgrading them to 5m).
func normalizeCacheControlTTL(payload []byte) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	original := payload
	seen5m := false
	modified := false

	processBlock := func(path string, obj gjson.Result) {
		cc := obj.Get("cache_control")
		if !cc.Exists() {
			return
		}
		if !cc.IsObject() {
			seen5m = true
			return
		}
		ttl := cc.Get("ttl")
		if ttl.Type != gjson.String || ttl.String() != "1h" {
			seen5m = true
			return
		}
		if !seen5m {
			return
		}
		ttlPath := path + ".cache_control.ttl"
		updated, errDel := sjson.DeleteBytes(payload, ttlPath)
		if errDel != nil {
			return
		}
		payload = updated
		modified = true
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			processBlock(fmt.Sprintf("tools.%d", int(idx.Int())), item)
			return true
		})
	}

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			processBlock(fmt.Sprintf("system.%d", int(idx.Int())), item)
			return true
		})
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgIdx, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				processBlock(fmt.Sprintf("messages.%d.content.%d", int(msgIdx.Int()), int(itemIdx.Int())), item)
				return true
			})
			return true
		})
	}

	if !modified {
		return original
	}
	return payload
}

// enforceCacheControlLimit removes excess cache_control blocks from a payload
// so the total does not exceed the Anthropic API limit (currently 4).
//
// Anthropic evaluates cache breakpoints in order: tools → system → messages.
// The most valuable breakpoints are:
//  1. Last tool         — caches ALL tool definitions
//  2. Last system block — caches ALL system content
//  3. Recent messages   — cache conversation context
//
// Removal priority (strip lowest-value first):
//
//	Phase 1: system blocks earliest-first, preserving the last one.
//	Phase 2: tool blocks earliest-first, preserving the last one.
//	Phase 3: message content blocks earliest-first.
//	Phase 4: remaining system blocks (last system).
//	Phase 5: remaining tool blocks (last tool).
func enforceCacheControlLimit(payload []byte, maxBlocks int) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	total := countCacheControls(payload)
	if total <= maxBlocks {
		return payload
	}

	excess := total - maxBlocks

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		lastIdx := -1
		system.ForEach(func(idx, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				lastIdx = int(idx.Int())
			}
			return true
		})
		if lastIdx >= 0 {
			system.ForEach(func(idx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				i := int(idx.Int())
				if i == lastIdx {
					return true
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("system.%d.cache_control", i)
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
		}
	}
	if excess <= 0 {
		return payload
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		lastIdx := -1
		tools.ForEach(func(idx, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				lastIdx = int(idx.Int())
			}
			return true
		})
		if lastIdx >= 0 {
			tools.ForEach(func(idx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				i := int(idx.Int())
				if i == lastIdx {
					return true
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("tools.%d.cache_control", i)
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
		}
	}
	if excess <= 0 {
		return payload
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgIdx, msg gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("messages.%d.content.%d.cache_control", int(msgIdx.Int()), int(itemIdx.Int()))
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
			return true
		})
	}
	if excess <= 0 {
		return payload
	}

	system = gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			if !item.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("system.%d.cache_control", int(idx.Int()))
			updated, errDel := sjson.DeleteBytes(payload, path)
			if errDel != nil {
				return true
			}
			payload = updated
			excess--
			return true
		})
	}
	if excess <= 0 {
		return payload
	}

	tools = gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			if !item.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("tools.%d.cache_control", int(idx.Int()))
			updated, errDel := sjson.DeleteBytes(payload, path)
			if errDel != nil {
				return true
			}
			payload = updated
			excess--
			return true
		})
	}

	return payload
}

// injectMessagesCacheControl adds cache_control to the second-to-last user turn for multi-turn caching.
// Per Anthropic docs: "Place cache_control on the second-to-last User message to let the model reuse the earlier cache."
// This enables caching of conversation history, which is especially beneficial for long multi-turn conversations.
// Only adds cache_control if:
// - There are at least 2 user turns in the conversation
// - No message content already has cache_control
func injectMessagesCacheControl(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	// Check if ANY message content already has cache_control
	hasCacheControlInMessages := false
	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, item gjson.Result) bool {
				if item.Get("cache_control").Exists() {
					hasCacheControlInMessages = true
					return false
				}
				return true
			})
		}
		return !hasCacheControlInMessages
	})
	if hasCacheControlInMessages {
		return payload
	}

	// Find all user message indices
	var userMsgIndices []int
	messages.ForEach(func(index gjson.Result, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			userMsgIndices = append(userMsgIndices, int(index.Int()))
		}
		return true
	})

	// Need at least 2 user turns to cache the second-to-last
	if len(userMsgIndices) < 2 {
		return payload
	}

	// Get the second-to-last user message index
	secondToLastUserIdx := userMsgIndices[len(userMsgIndices)-2]

	// Get the content of this message
	contentPath := fmt.Sprintf("messages.%d.content", secondToLastUserIdx)
	content := gjson.GetBytes(payload, contentPath)

	if content.IsArray() {
		// Add cache_control to the last content block of this message
		contentCount := int(content.Get("#").Int())
		if contentCount > 0 {
			cacheControlPath := fmt.Sprintf("messages.%d.content.%d.cache_control", secondToLastUserIdx, contentCount-1)
			result, err := sjson.SetBytes(payload, cacheControlPath, map[string]string{"type": "ephemeral"})
			if err != nil {
				log.Warnf("failed to inject cache_control into messages: %v", err)
				return payload
			}
			payload = result
		}
	} else if content.Type == gjson.String {
		// Convert string content to array with cache_control
		text := content.String()
		newContent := []map[string]interface{}{
			{
				"type": "text",
				"text": text,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			},
		}
		result, err := sjson.SetBytes(payload, contentPath, newContent)
		if err != nil {
			log.Warnf("failed to inject cache_control into message string content: %v", err)
			return payload
		}
		payload = result
	}

	return payload
}

// injectToolsCacheControl adds cache_control to the last non-deferred tool in the tools array.
// Deferred tools cannot use prompt caching, so trailing deferred tools are skipped.
// This only adds cache_control if NO tool in the array already has it.
func injectToolsCacheControl(payload []byte) []byte {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return payload
	}

	// Check if ANY tool already has cache_control and find the last eligible tool.
	hasCacheControlInTools := false
	lastEligibleToolIndex := -1
	tools.ForEach(func(index, tool gjson.Result) bool {
		if tool.Get("cache_control").Exists() {
			hasCacheControlInTools = true
			return false
		}
		if !tool.Get("defer_loading").Bool() {
			lastEligibleToolIndex = int(index.Int())
		}
		return true
	})
	if hasCacheControlInTools || lastEligibleToolIndex < 0 {
		return payload
	}

	lastToolPath := fmt.Sprintf("tools.%d.cache_control", lastEligibleToolIndex)
	result, err := sjson.SetBytes(payload, lastToolPath, map[string]string{"type": "ephemeral"})
	if err != nil {
		log.Warnf("failed to inject cache_control into tools array: %v", err)
		return payload
	}

	return result
}

// injectSystemCacheControl adds cache_control to the last element in the system prompt.
// Converts string system prompts to array format if needed.
// This only adds cache_control if NO system element already has it.
func injectSystemCacheControl(payload []byte) []byte {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return payload
	}

	if system.IsArray() {
		count := int(system.Get("#").Int())
		if count == 0 {
			return payload
		}

		// Check if ANY system element already has cache_control
		hasCacheControlInSystem := false
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				hasCacheControlInSystem = true
				return false
			}
			return true
		})
		if hasCacheControlInSystem {
			return payload
		}

		// Add cache_control to the last system element
		lastSystemPath := fmt.Sprintf("system.%d.cache_control", count-1)
		result, err := sjson.SetBytes(payload, lastSystemPath, map[string]string{"type": "ephemeral"})
		if err != nil {
			log.Warnf("failed to inject cache_control into system array: %v", err)
			return payload
		}
		payload = result
	} else if system.Type == gjson.String {
		// Convert string system prompt to array with cache_control
		// "system": "text" -> "system": [{"type": "text", "text": "text", "cache_control": {"type": "ephemeral"}}]
		text := system.String()
		newSystem := []map[string]interface{}{
			{
				"type": "text",
				"text": text,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			},
		}
		result, err := sjson.SetBytes(payload, "system", newSystem)
		if err != nil {
			log.Warnf("failed to inject cache_control into system string: %v", err)
			return payload
		}
		payload = result
	}

	return payload
}

func ensureModelMaxTokens(body []byte, modelID string) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	if maxTokens := gjson.GetBytes(body, "max_tokens"); maxTokens.Exists() {
		return body
	}

	for _, provider := range registry.GetGlobalRegistry().GetModelProviders(strings.TrimSpace(modelID)) {
		if strings.EqualFold(provider, "claude") {
			maxTokens := defaultModelMaxTokens
			if info := registry.GetGlobalRegistry().GetModelInfo(strings.TrimSpace(modelID), "claude"); info != nil && info.MaxCompletionTokens > 0 {
				maxTokens = info.MaxCompletionTokens
			}
			body, _ = sjson.SetBytes(body, "max_tokens", maxTokens)
			return body
		}
	}

	return body
}

// Package openai provides request translation from OpenAI Chat Completions to Kiro format.
// It handles parsing and transforming OpenAI API requests into the Kiro/Amazon Q API format,
// extracting model information, system instructions, message contents, and tool declarations.
package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	kiroclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/claude"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/common"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Kiro API request structs - reuse from kiroclaude package structure

// KiroPayload is the top-level request structure for Kiro API
type KiroPayload struct {
	ConversationState            KiroConversationState `json:"conversationState"`
	ProfileArn                   string                `json:"profileArn,omitempty"`
	InferenceConfig              *KiroInferenceConfig  `json:"inferenceConfig,omitempty"`
	AdditionalModelRequestFields map[string]any        `json:"additionalModelRequestFields,omitempty"`
}

// KiroInferenceConfig contains inference parameters for the Kiro API.
type KiroInferenceConfig struct {
	MaxTokens   int     `json:"maxTokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"topP,omitempty"`
}

// KiroConversationState holds the conversation context
type KiroConversationState struct {
	AgentContinuationID string               `json:"agentContinuationId,omitempty"`
	AgentTaskType       string               `json:"agentTaskType,omitempty"`
	ChatTriggerType     string               `json:"chatTriggerType"` // Required: "MANUAL"
	ConversationID      string               `json:"conversationId"`
	CurrentMessage      KiroCurrentMessage   `json:"currentMessage"`
	History             []KiroHistoryMessage `json:"history,omitempty"`
}

// KiroCurrentMessage wraps the current user message
type KiroCurrentMessage struct {
	UserInputMessage KiroUserInputMessage `json:"userInputMessage"`
}

// KiroHistoryMessage represents a message in the conversation history
type KiroHistoryMessage struct {
	UserInputMessage         *KiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *KiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

type KiroImage = kirocommon.KiroImage
type KiroImageSource = kirocommon.KiroImageSource

// KiroUserInputMessage represents a user message
type KiroUserInputMessage struct {
	Content                 string                       `json:"content"`
	ModelID                 string                       `json:"modelId"`
	Origin                  string                       `json:"origin"`
	Images                  []KiroImage                  `json:"images,omitempty"`
	UserInputMessageContext *KiroUserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// KiroUserInputMessageContext contains tool-related context
type KiroUserInputMessageContext struct {
	ToolResults []KiroToolResult  `json:"toolResults,omitempty"`
	Tools       []KiroToolWrapper `json:"tools,omitempty"`
}

// KiroToolResult represents a tool execution result
type KiroToolResult struct {
	Content   []KiroTextContent `json:"content"`
	Status    string            `json:"status"`
	ToolUseID string            `json:"toolUseId"`
}

// KiroTextContent represents text content
type KiroTextContent struct {
	Text string `json:"text"`
}

// KiroToolWrapper wraps a tool specification
type KiroToolWrapper struct {
	ToolSpecification KiroToolSpecification `json:"toolSpecification"`
}

// KiroToolSpecification defines a tool's schema
type KiroToolSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema KiroInputSchema `json:"inputSchema"`
}

// KiroInputSchema wraps the JSON schema for tool input
type KiroInputSchema struct {
	JSON interface{} `json:"json"`
}

// KiroAssistantResponseMessage represents an assistant message
type KiroAssistantResponseMessage struct {
	Content          string                `json:"content"`
	ToolUses         []KiroToolUse         `json:"toolUses,omitempty"`
	ReasoningContent *KiroReasoningContent `json:"reasoningContent,omitempty"`
}

// KiroReasoningContent is the nested reasoning envelope Kiro accepts for
// signed prior-turn thinking. Mirrors kiro-lb build_kiro_history.
type KiroReasoningContent struct {
	ReasoningText KiroReasoningText `json:"reasoningText"`
}

// KiroReasoningText carries the reasoning prose and its required signature.
type KiroReasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature"`
}

// KiroToolUse represents a tool invocation by the assistant
type KiroToolUse struct {
	ToolUseID string                 `json:"toolUseId"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
}

// ConvertOpenAIRequestToKiro converts an OpenAI Chat Completions request to Kiro format.
// This is the main entry point for request translation.
// Note: The actual payload building happens in the executor, this just passes through
// the OpenAI format which will be converted by BuildKiroPayloadFromOpenAI.
func ConvertOpenAIRequestToKiro(modelName string, inputRawJSON []byte, stream bool) []byte {
	// Pass through the OpenAI format - actual conversion happens in BuildKiroPayloadFromOpenAI
	return inputRawJSON
}

// BuildKiroPayloadFromOpenAI constructs the Kiro API request payload from OpenAI format.
// Supports tool calling - tools are passed via userInputMessageContext.
// origin parameter determines which quota to use: "CLI" for Amazon Q, "AI_EDITOR" for Kiro IDE.
// isAgentic parameter enables chunked write optimization prompt for -agentic model variants.
// isChatOnly parameter disables tool calling for -chat model variants (pure conversation mode).
// headers parameter allows checking Anthropic-Beta header for thinking mode detection.
// metadata parameter is kept for API compatibility but no longer used for thinking configuration.
// Returns the payload and a boolean indicating whether thinking mode was injected.
func BuildKiroPayloadFromOpenAI(openaiBody []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool, headers http.Header, metadata map[string]any) ([]byte, bool) {
	// Extract max_tokens for potential use in inferenceConfig
	// Handle -1 as "use maximum" (Kiro max output is ~32000 tokens)
	const kiroMaxOutputTokens = 32000
	var maxTokens int64
	if mt := gjson.GetBytes(openaiBody, "max_tokens"); mt.Exists() {
		maxTokens = mt.Int()
		if maxTokens == -1 {
			maxTokens = kiroMaxOutputTokens
			log.Debugf("kiro-openai: max_tokens=-1 converted to %d", kiroMaxOutputTokens)
		}
	}

	// Extract temperature if specified
	var temperature float64
	var hasTemperature bool
	if temp := gjson.GetBytes(openaiBody, "temperature"); temp.Exists() {
		temperature = temp.Float()
		hasTemperature = true
	}

	// Extract top_p if specified
	var topP float64
	var hasTopP bool
	if tp := gjson.GetBytes(openaiBody, "top_p"); tp.Exists() {
		topP = tp.Float()
		hasTopP = true
		log.Debugf("kiro-openai: extracted top_p: %.2f", topP)
	}

	// Normalize origin value for Kiro API compatibility
	origin = normalizeOrigin(origin)
	log.Debugf("kiro-openai: normalized origin value: %s", origin)

	messages := gjson.GetBytes(openaiBody, "messages")

	// For chat-only mode, don't include tools
	var tools gjson.Result
	if !isChatOnly {
		tools = gjson.GetBytes(openaiBody, "tools")
	}

	// Extract system prompt from messages
	systemPrompt := extractSystemPromptFromOpenAI(messages)

	// Inject timestamp context
	timestamp := time.Now().Format("2006-01-02 15:04:05 MST")
	timestampContext := fmt.Sprintf("[Context: Current time is %s]", timestamp)
	if systemPrompt != "" {
		systemPrompt = timestampContext + "\n\n" + systemPrompt
	} else {
		systemPrompt = timestampContext
	}
	log.Debugf("kiro-openai: injected timestamp context: %s", timestamp)

	// Inject agentic optimization prompt for -agentic model variants
	if isAgentic {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += kirocommon.KiroAgenticSystemPrompt
	}

	// Handle tool_choice parameter - Kiro doesn't support it natively, so we inject system prompt hints
	// OpenAI tool_choice values: "none", "auto", "required", or {"type":"function","function":{"name":"..."}}
	toolChoiceHint := extractToolChoiceHint(openaiBody)
	if toolChoiceHint != "" {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += toolChoiceHint
		log.Debugf("kiro-openai: injected tool_choice hint into system prompt")
	}

	// Handle response_format parameter - Kiro doesn't support it natively, so we inject system prompt hints
	// OpenAI response_format: {"type": "json_object"} or {"type": "json_schema", "json_schema": {...}}
	responseFormatHint := extractResponseFormatHint(openaiBody)
	if responseFormatHint != "" {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += responseFormatHint
		log.Debugf("kiro-openai: injected response_format hint into system prompt")
	}

	// Check for thinking mode
	// Supports OpenAI reasoning_effort parameter, model name hints, and Anthropic-Beta header
	thinkingEnabled := checkThinkingModeFromOpenAIWithHeaders(openaiBody, headers)

	// Convert OpenAI tools to Kiro format
	kiroTools, toolDocumentation := convertOpenAIToolsToKiro(tools, modelID)
	if toolDocumentation != "" {
		if systemPrompt != "" {
			systemPrompt += "\n\n"
		}
		systemPrompt += toolDocumentation
	}

	// Thinking mode: prompt tags stay available for existing models; the
	// additionalModelRequestFields envelope is gated to OmniRoute's
	// adaptive/native allowlists (claude-sonnet-5 / gpt-5.6-*).
	effort := ""
	if reasoning := gjson.GetBytes(openaiBody, "reasoning_effort"); reasoning.Exists() {
		effort = reasoning.String()
	}
	if effort == "" {
		if oc := gjson.GetBytes(openaiBody, "output_config.effort"); oc.Exists() {
			effort = oc.String()
		}
	}
	if effort == "" {
		thinkingType := gjson.GetBytes(openaiBody, "thinking.type").String()
		switch thinkingType {
		case "adaptive":
			effort = "high"
		case "enabled":
			effort = kirocommon.EffortFromThinkingBudget(gjson.GetBytes(openaiBody, "thinking.budget_tokens").Int())
		}
	}
	thinkingPlan := kirocommon.PlanKiroThinking(modelID, thinkingEnabled, effort, maxTokens)
	if thinkingPlan.InjectPrompt {
		thinkingHint := kirocommon.ThinkingDirective(thinkingPlan.ThinkingLength)
		if systemPrompt != "" {
			systemPrompt = thinkingHint + "\n\n" + systemPrompt
		} else {
			systemPrompt = thinkingHint
		}
		log.Infof("kiro-openai: injected thinking prompt (official mode), has_tools: %v", len(kiroTools) > 0)
	}

	// Process messages and build history
	// If no tools are defined, convert tool content to text so no toolResults
	// are sent without tool definitions (kiro-lb strip_all_tool_content). Kiro
	// rejects toolResults that reference no tool definitions.
	if len(kiroTools) == 0 {
		if stripped := stripAllToolContent(messages.Array()); stripped != nil {
			messages = gjson.ParseBytes(stripped)
		}
	}
	// Tool content is already stripped above when no tools are defined, so
	// structured tool blocks stay allowed here.
	history, currentUserMsg, currentToolResults := processOpenAIMessages(messages, modelID, origin, true)

	// Build content with system prompt
	if currentUserMsg != nil {
		currentUserMsg.Content = buildFinalContent(currentUserMsg.Content, systemPrompt, currentToolResults)

		// Deduplicate currentToolResults
		currentToolResults = deduplicateToolResults(currentToolResults)

		// Build userInputMessageContext with tools and tool results
		if len(kiroTools) > 0 || len(currentToolResults) > 0 {
			currentUserMsg.UserInputMessageContext = &KiroUserInputMessageContext{
				Tools:       kiroTools,
				ToolResults: currentToolResults,
			}
		}
	}

	// Build payload
	var currentMessage KiroCurrentMessage
	if currentUserMsg != nil {
		currentMessage = KiroCurrentMessage{UserInputMessage: *currentUserMsg}
	} else {
		fallbackContent := ""
		if systemPrompt != "" {
			fallbackContent = "--- SYSTEM PROMPT ---\n" + systemPrompt + "\n--- END SYSTEM PROMPT ---\n"
		}
		currentMessage = KiroCurrentMessage{UserInputMessage: KiroUserInputMessage{
			Content: fallbackContent,
			ModelID: modelID,
			Origin:  origin,
		}}
	}

	// Build inferenceConfig if we have any inference parameters
	// Note: Kiro API doesn't actually use max_tokens for thinking budget
	var inferenceConfig *KiroInferenceConfig
	if maxTokens > 0 || hasTemperature || hasTopP {
		inferenceConfig = &KiroInferenceConfig{}
		if maxTokens > 0 {
			inferenceConfig.MaxTokens = int(maxTokens)
		}
		if hasTemperature {
			inferenceConfig.Temperature = temperature
		}
		if hasTopP {
			inferenceConfig.TopP = topP
		}
	}

	// Session IDs: extract from messages[].additional_kwargs (LangChain format) or random
	conversationID := extractMetadataFromMessages(messages, "conversationId")
	continuationID := extractMetadataFromMessages(messages, "continuationId")
	if conversationID == "" {
		conversationID = uuid.New().String()
	}

	payload := KiroPayload{
		ConversationState: KiroConversationState{
			AgentTaskType:   "vibe",
			ChatTriggerType: "MANUAL",
			ConversationID:  conversationID,
			CurrentMessage:  currentMessage,
			History:         history,
		},
		ProfileArn:                   profileArn,
		InferenceConfig:              inferenceConfig,
		AdditionalModelRequestFields: thinkingPlan.Fields,
	}
	if thinkingPlan.AdaptiveThinking && payload.InferenceConfig != nil {
		payload.InferenceConfig.Temperature = 0
		payload.InferenceConfig.TopP = 0
		if payload.InferenceConfig.MaxTokens == 0 {
			payload.InferenceConfig = nil
		}
	}

	// Only set AgentContinuationID if client provided
	if continuationID != "" {
		payload.ConversationState.AgentContinuationID = continuationID
	}

	result, err := json.Marshal(payload)
	if err != nil {
		log.Debugf("kiro-openai: failed to marshal payload: %v", err)
		return nil, false
	}

	return result, thinkingEnabled
}

// normalizeOrigin normalizes origin value for Kiro API compatibility
func normalizeOrigin(origin string) string {
	switch origin {
	case "KIRO_CLI":
		return "CLI"
	case "KIRO_AI_EDITOR":
		return "AI_EDITOR"
	case "AMAZON_Q":
		return "CLI"
	case "KIRO_IDE":
		return "AI_EDITOR"
	default:
		return origin
	}
}

// extractMetadataFromMessages extracts metadata from messages[].additional_kwargs (LangChain format).
// Searches from the last message backwards, returns empty string if not found.
func extractMetadataFromMessages(messages gjson.Result, key string) string {
	arr := messages.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		if val := arr[i].Get("additional_kwargs." + key); val.Exists() && val.String() != "" {
			return val.String()
		}
	}
	return ""
}

// extractSystemPromptFromOpenAI extracts system prompt from OpenAI messages.
// Both "system" and "developer" roles are folded into the system prompt,
// matching kiro-lb convert_openai_messages_to_unified (which treats
// (system, developer) identically as prompt fragments).
func extractSystemPromptFromOpenAI(messages gjson.Result) string {
	if !messages.IsArray() {
		return ""
	}

	var systemParts []string
	for _, msg := range messages.Array() {
		role := msg.Get("role").String()
		if role == "system" || role == "developer" {
			content := msg.Get("content")
			if content.Type == gjson.String {
				systemParts = append(systemParts, content.String())
			} else if content.IsArray() {
				// Handle array content format
				for _, part := range content.Array() {
					if part.Get("type").String() == "text" {
						systemParts = append(systemParts, part.Get("text").String())
					}
				}
			}
		}
	}

	return strings.Join(systemParts, "\n")
}

// shortenToolNameIfNeeded shortens tool names that exceed 64 characters.
// MCP tools often have long names like "mcp__server-name__tool-name".
// This preserves the "mcp__" prefix and last segment when possible.
func shortenToolNameIfNeeded(name string) string {
	return kirocommon.NormalizeKiroToolName(name)
}

func ensureKiroInputSchema(parameters interface{}) interface{} {
	if parameters != nil {
		return parameters
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

// convertOpenAIToolsToKiro converts OpenAI tools to Kiro format
func convertOpenAIToolsToKiro(tools gjson.Result, modelID string) ([]KiroToolWrapper, string) {
	var kiroTools []KiroToolWrapper
	var toolDocumentation []string
	if !tools.IsArray() {
		return kiroTools, ""
	}

	catalogBytes := 0
	for _, tool := range tools.Array() {
		if len(kiroTools) >= kirocommon.KiroMaxToolCount {
			break
		}
		// OpenAI tools have type "function" with function definition inside
		if tool.Get("type").String() != "function" {
			continue
		}

		fn := tool.Get("function")
		if !fn.Exists() {
			continue
		}

		name := fn.Get("name").String()
		description := fn.Get("description").String()
		parametersResult := fn.Get("parameters")
		var parameters interface{}
		if parametersResult.Exists() && parametersResult.Type != gjson.Null {
			parameters = parametersResult.Value()
		}
		parameters = ensureKiroInputSchema(parameters)
		if schema, ok := parameters.(map[string]interface{}); ok {
			parameters = kirocommon.SanitizeKiroToolSchema(schema)
		}

		// Shorten tool name if it exceeds 64 characters (common with MCP tools)
		originalName := name
		name = shortenToolNameIfNeeded(name)
		if name != originalName {
			log.Debugf("kiro-openai: shortened tool name from '%s' to '%s'", originalName, name)
		}

		// CRITICAL FIX: Kiro API requires non-empty description
		if strings.TrimSpace(description) == "" {
			description = fmt.Sprintf("Tool: %s", name)
			log.Debugf("kiro-openai: tool '%s' has empty description, using default: %s", name, description)
		}

		// Truncate long descriptions
		description, movedDocumentation := kirocommon.PrepareKiroToolDescription(name, description, modelID)
		if movedDocumentation != "" {
			toolDocumentation = append(toolDocumentation, movedDocumentation)
		}

		candidate := KiroToolWrapper{
			ToolSpecification: KiroToolSpecification{
				Name:        name,
				Description: description,
				InputSchema: KiroInputSchema{JSON: parameters},
			},
		}
		encoded, _ := json.Marshal(candidate)
		if len(kiroTools) > 0 && catalogBytes+len(encoded) > kirocommon.KiroToolCatalogBudgetBytes {
			break
		}
		catalogBytes += len(encoded)
		kiroTools = append(kiroTools, candidate)
	}

	return kiroTools, strings.Join(toolDocumentation, "\n\n")
}

// processOpenAIMessages processes OpenAI messages and builds Kiro history
func processOpenAIMessages(messages gjson.Result, modelID, origin string, allowStructuredTools bool) ([]KiroHistoryMessage, *KiroUserInputMessage, []KiroToolResult) {
	var history []KiroHistoryMessage
	var currentUserMsg *KiroUserInputMessage
	var currentToolResults []KiroToolResult

	if !messages.IsArray() {
		return history, currentUserMsg, currentToolResults
	}

	// Roles Kiro does not understand must become user before merging, so
	// consecutive unknown-role turns are folded into one user turn.
	messagesArray := kirocommon.MergeAdjacentMessages(normalizeRoles(messages.Array()))

	// Track pending tool results that should be attached to the next user message
	// This is critical for LiteLLM-translated requests where tool results appear
	// as separate "tool" role messages between assistant and user messages
	var pendingToolResults []KiroToolResult
	var pendingToolImages []KiroImage
	var pendingToolText []string

	for i, msg := range messagesArray {
		role := msg.Get("role").String()
		isLastMessage := i == len(messagesArray)-1

		switch role {
		case "system":
			// System messages are handled separately via extractSystemPromptFromOpenAI
			continue

		case "user":
			userMsg, toolResults := buildUserMessageFromOpenAI(msg, modelID, origin)
			if len(pendingToolText) > 0 {
				userMsg.Content = strings.Join(pendingToolText, "\n") + "\n" + userMsg.Content
				pendingToolText = nil
			}
			// Merge any pending tool results from preceding "tool" role messages
			toolResults = append(pendingToolResults, toolResults...)
			userMsg.Images = append(pendingToolImages, userMsg.Images...)
			userMsg.Images, _ = kirocommon.LimitKiroImages(userMsg.Images)
			pendingToolResults = nil // Reset pending tool results
			pendingToolImages = nil

			if isLastMessage {
				currentUserMsg = &userMsg
				currentToolResults = toolResults
			} else {
				// CRITICAL: Kiro API requires content to be non-empty for history messages
				if strings.TrimSpace(userMsg.Content) == "" {
					if len(toolResults) > 0 {
						userMsg.Content = "Tool results provided."
					} else {
						userMsg.Content = "Continue"
					}
				}
				// For history messages, embed tool results in context
				if len(toolResults) > 0 {
					userMsg.UserInputMessageContext = &KiroUserInputMessageContext{
						ToolResults: toolResults,
					}
				}
				history = append(history, KiroHistoryMessage{
					UserInputMessage: &userMsg,
				})
			}

		case "assistant":
			assistantMsg := buildAssistantMessageFromOpenAI(msg, allowStructuredTools)

			// If there are pending tool results, we need to insert a synthetic user message
			// before this assistant message to maintain proper conversation structure
			if len(pendingToolResults) > 0 {
				syntheticUserMsg := KiroUserInputMessage{
					Content: "Tool results provided.",
					ModelID: modelID,
					Origin:  origin,
					UserInputMessageContext: &KiroUserInputMessageContext{
						ToolResults: pendingToolResults,
					},
					Images: pendingToolImages,
				}
				history = append(history, KiroHistoryMessage{
					UserInputMessage: &syntheticUserMsg,
				})
				pendingToolResults = nil
				pendingToolImages = nil
			}

			if isLastMessage {
				history = append(history, KiroHistoryMessage{
					AssistantResponseMessage: &assistantMsg,
				})
				// Create a "Continue" user message as currentMessage
				currentUserMsg = &KiroUserInputMessage{
					Content: "Continue",
					ModelID: modelID,
					Origin:  origin,
				}
			} else {
				history = append(history, KiroHistoryMessage{
					AssistantResponseMessage: &assistantMsg,
				})
			}

		case "tool":
			// Tool messages in OpenAI format provide results for tool_calls
			// These are typically followed by user or assistant messages
			// Collect them as pending and attach to the next user message
			toolCallID := msg.Get("tool_call_id").String()
			contentResult := msg.Get("content")
			content := contentResult.String()
			if contentResult.IsArray() {
				var contentBuilder strings.Builder
				for _, part := range contentResult.Array() {
					switch part.Get("type").String() {
					case "text":
						contentBuilder.WriteString(part.Get("text").String())
					case "image_url":
						if image, ok := openAIImageFromDataURL(part.Get("image_url.url").String()); ok {
							pendingToolImages = append(pendingToolImages, image)
						}
					}
				}
				content = contentBuilder.String()
			}
			if !allowStructuredTools {
				pendingToolText = append(pendingToolText, fmt.Sprintf(
					"[Tool result %s: %s]",
					kirocommon.NormalizeKiroToolUseID(toolCallID),
					content,
				))
				continue
			}

			if toolCallID != "" {
				toolResult := KiroToolResult{
					ToolUseID: kirocommon.NormalizeKiroToolUseID(toolCallID),
					Content:   []KiroTextContent{{Text: content}},
					Status:    "success",
				}
				// Collect pending tool results to attach to the next user message
				pendingToolResults = append(pendingToolResults, toolResult)
			}
		}
	}

	// Handle case where tool results are at the end with no following user message
	if len(pendingToolResults) > 0 {
		currentToolResults = append(currentToolResults, pendingToolResults...)
		// If there's no current user message, create a synthetic one for the tool results
		if currentUserMsg == nil {
			currentUserMsg = &KiroUserInputMessage{
				Content: "Tool results provided.",
				ModelID: modelID,
				Origin:  origin,
			}
		}
	}
	if currentUserMsg != nil && len(pendingToolImages) > 0 {
		currentUserMsg.Images = append(pendingToolImages, currentUserMsg.Images...)
		currentUserMsg.Images, _ = kirocommon.LimitKiroImages(currentUserMsg.Images)
	}
	if len(pendingToolText) > 0 {
		text := strings.Join(pendingToolText, "\n")
		if currentUserMsg == nil {
			currentUserMsg = &KiroUserInputMessage{Content: text, ModelID: modelID, Origin: origin}
		} else {
			currentUserMsg.Content = text + "\n" + currentUserMsg.Content
		}
	}

	// Truncate history if too long to prevent Kiro API errors
	history = truncateHistoryIfNeeded(history)

	// Ensure history alternates and starts with a user message (kiro-lb
	// ensure_first_message_is_user + ensure_alternating_roles). Kiro rejects
	// a history that starts with assistant or has consecutive userInputMessage
	// entries ("Improperly formed request").
	history = ensureFirstMessageIsUserHistory(history, modelID, origin)
	history = ensureAlternatingHistory(history)

	history, currentUserMsg, currentToolResults = filterOrphanedToolResults(history, currentUserMsg, currentToolResults)

	return history, currentUserMsg, currentToolResults
}

const kiroMaxHistoryMessages = 50

// normalizeRoles rewrites roles Kiro does not accept (anything other than
// user/assistant/system/developer/tool) to "user", mirroring kiro-lb
// normalize_message_roles. Kiro only supports user and assistant in history;
// any other role must become user so it is never silently dropped.
func normalizeRoles(messages []gjson.Result) []gjson.Result {
	out := make([]gjson.Result, 0, len(messages))
	for _, msg := range messages {
		role := msg.Get("role").String()
		switch role {
		case "user", "assistant", "system", "developer", "tool":
			out = append(out, msg)
		default:
			log.Debugf("kiro-openai: normalizing unknown role '%s' to user", role)
			if m, ok := msg.Value().(map[string]interface{}); ok {
				m["role"] = "user"
				if b, err := json.Marshal(m); err == nil {
					out = append(out, gjson.ParseBytes(b))
					continue
				}
			}
			out = append(out, msg)
		}
	}
	return out
}

// ensureFirstMessageIsUserHistory prepends a placeholder user entry when the
// history would otherwise start with an assistant turn. Mirrors kiro-lb
// ensure_first_message_is_user (and the claude converter's placeholder
// behavior): Kiro requires the history to begin with a userInputMessage.
func ensureFirstMessageIsUserHistory(history []KiroHistoryMessage, modelID, origin string) []KiroHistoryMessage {
	if len(history) > 0 && history[0].UserInputMessage == nil {
		log.Debugf("kiro-openai: history started with assistant, prepending placeholder user message")
		placeholder := KiroHistoryMessage{
			UserInputMessage: &KiroUserInputMessage{
				Content: ".",
				ModelID: modelID,
				Origin:  origin,
			},
		}
		return append([]KiroHistoryMessage{placeholder}, history...)
	}
	return history
}

// ensureAlternatingHistory inserts a synthetic assistant entry between
// consecutive userInputMessage entries, mirroring kiro-lb
// ensure_alternating_roles. Kiro rejects two consecutive user messages in
// history; the synthetic assistant carries neutral text so it is never read
// as a real model response.
func ensureAlternatingHistory(history []KiroHistoryMessage) []KiroHistoryMessage {
	if len(history) < 2 {
		return history
	}
	var out []KiroHistoryMessage
	for _, h := range history {
		if len(out) > 0 && h.UserInputMessage != nil && out[len(out)-1].UserInputMessage != nil {
			out = append(out, KiroHistoryMessage{
				AssistantResponseMessage: &KiroAssistantResponseMessage{
					Content: ".",
				},
			})
		}
		out = append(out, h)
	}
	return out
}

func truncateHistoryIfNeeded(history []KiroHistoryMessage) []KiroHistoryMessage {
	if len(history) <= kiroMaxHistoryMessages {
		return history
	}

	log.Debugf("kiro-openai: truncating history from %d to %d messages", len(history), kiroMaxHistoryMessages)
	return history[len(history)-kiroMaxHistoryMessages:]
}

// filterOrphanedToolResults removes tool results with no matching tool_use in
// retained history (compaction artifact), but preserves their text on the
// user message content instead of silently dropping it (matching kiro-lb's
// orphaned-tool-result repair). currentUserMsg is passed in so orphaned
// current-message tool results can also be preserved as text.
func filterOrphanedToolResults(history []KiroHistoryMessage, currentUserMsg *KiroUserInputMessage, currentToolResults []KiroToolResult) ([]KiroHistoryMessage, *KiroUserInputMessage, []KiroToolResult) {
	// Remove tool results with no matching tool_use in retained history.
	// This happens after truncation when the assistant turn that produced tool_use
	// is dropped but a later user/tool_result survives.
	validToolUseIDs := make(map[string]bool)
	for _, h := range history {
		if h.AssistantResponseMessage == nil {
			continue
		}
		for _, tu := range h.AssistantResponseMessage.ToolUses {
			validToolUseIDs[tu.ToolUseID] = true
		}
	}

	for i, h := range history {
		if h.UserInputMessage == nil || h.UserInputMessage.UserInputMessageContext == nil {
			continue
		}
		ctx := h.UserInputMessage.UserInputMessageContext
		if len(ctx.ToolResults) == 0 {
			continue
		}

		var filtered []KiroToolResult
		var orphanTexts []string
		for _, tr := range ctx.ToolResults {
			if validToolUseIDs[tr.ToolUseID] {
				filtered = append(filtered, tr)
				continue
			}
			orphanTexts = append(orphanTexts, kiroToolResultToText(tr))
			log.Debugf("kiro-openai: preserving orphaned tool_result in history[%d] as text: toolUseId=%s (no matching tool_use)", i, tr.ToolUseID)
		}
		ctx.ToolResults = filtered
		if len(ctx.ToolResults) == 0 && len(ctx.Tools) == 0 {
			h.UserInputMessage.UserInputMessageContext = nil
		}
		if len(orphanTexts) > 0 {
			h.UserInputMessage.Content = appendTextWithSep(h.UserInputMessage.Content, strings.Join(orphanTexts, "\n\n"))
		}
	}

	if len(currentToolResults) > 0 {
		var filtered []KiroToolResult
		var orphanTexts []string
		for _, tr := range currentToolResults {
			if validToolUseIDs[tr.ToolUseID] {
				filtered = append(filtered, tr)
				continue
			}
			orphanTexts = append(orphanTexts, kiroToolResultToText(tr))
			log.Debugf("kiro-openai: preserving orphaned tool_result in currentMessage as text: toolUseId=%s (no matching tool_use)", tr.ToolUseID)
		}
		if len(orphanTexts) > 0 && currentUserMsg != nil {
			currentUserMsg.Content = appendTextWithSep(currentUserMsg.Content, strings.Join(orphanTexts, "\n\n"))
		}
		if len(filtered) != len(currentToolResults) {
			log.Infof("kiro-openai: preserved %d orphaned tool_result(s) from currentMessage as text", len(currentToolResults)-len(filtered))
		}
		currentToolResults = filtered
	}

	return history, currentUserMsg, currentToolResults
}

// kiroToolResultToText renders a KiroToolResult as a human-readable text
// marker, mirroring kiro-lb tool_results_to_text.
func kiroToolResultToText(tr KiroToolResult) string {
	text := ""
	for _, c := range tr.Content {
		text += c.Text
	}
	if text == "" {
		text = "(empty result)"
	}
	if tr.ToolUseID != "" {
		return "[Tool Result (" + tr.ToolUseID + ")]\n" + text
	}
	return "[Tool Result]\n" + text
}

// appendTextWithSep joins new text to existing content with a double newline
// separator when both are non-empty.
func appendTextWithSep(existing, addition string) string {
	if strings.TrimSpace(existing) == "" {
		return addition
	}
	if addition == "" {
		return existing
	}
	return existing + "\n\n" + addition
}

// stripAllToolContent converts tool_calls and tool results into plain text
// when no tools are defined in the request, mirroring kiro-lb
// strip_all_tool_content. Kiro rejects toolResults that reference no tool
// definitions, so preserving the conversation as text keeps context without
// triggering that rejection. Tool messages are dropped (their text is
// buffered and attached to the following message); assistant tool_calls are
// appended to the assistant content.
func stripAllToolContent(messages []gjson.Result) []byte {
	if len(messages) == 0 {
		return nil
	}
	var out []map[string]interface{}
	var pendingResults []string
	for _, msg := range messages {
		m, ok := msg.Value().(map[string]interface{})
		if !ok {
			out = append(out, map[string]interface{}{
				"role":    msg.Get("role").String(),
				"content": msg.Get("content").String(),
			})
			continue
		}
		role := msg.Get("role").String()
		switch role {
		case "tool":
			text := msg.Get("content").String()
			id := msg.Get("tool_call_id").String()
			if text != "" || id != "" {
				label := "Tool Result"
				if id != "" {
					label = "Tool Result (" + id + ")"
				}
				pendingResults = append(pendingResults, "["+label+"]\n"+text)
			}
			// Tool message is dropped; its text rides the next message.
			continue
		case "assistant":
			var toolTexts []string
			if tc := msg.Get("tool_calls"); tc.IsArray() {
				for _, call := range tc.Array() {
					name := call.Get("function.name").String()
					args := call.Get("function.arguments").String()
					id := call.Get("id").String()
					if name == "" {
						name = "unknown"
					}
					label := "Tool: " + name
					if id != "" {
						label += " (" + id + ")"
					}
					toolTexts = append(toolTexts, "["+label+"]\n"+args)
				}
			}
			contentText := contentTextFromValue(m["content"])
			if len(toolTexts) > 0 {
				if contentText != "" {
					m["content"] = contentText + "\n\n" + strings.Join(toolTexts, "\n\n")
				} else {
					m["content"] = strings.Join(toolTexts, "\n\n")
				}
				delete(m, "tool_calls")
			}
			attachPendingResults(m, &pendingResults)
			out = append(out, m)
		default:
			attachPendingResults(m, &pendingResults)
			out = append(out, m)
		}
	}
	// Trailing tool results with no following message become a user message.
	if len(pendingResults) > 0 {
		out = append(out, map[string]interface{}{
			"role":    "user",
			"content": strings.Join(pendingResults, "\n\n"),
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

// attachPendingResults appends buffered tool-result text to a message's
// content and clears the buffer.
func attachPendingResults(m map[string]interface{}, pending *[]string) {
	if len(*pending) == 0 {
		return
	}
	appendText := strings.Join(*pending, "\n\n")
	contentText := contentTextFromValue(m["content"])
	if contentText != "" {
		m["content"] = contentText + "\n\n" + appendText
	} else {
		m["content"] = appendText
	}
	*pending = nil
}

// contentTextFromValue extracts text from a message content value, handling
// both plain strings and arrays of content blocks.
func contentTextFromValue(v interface{}) string {
	switch c := v.(type) {
	case string:
		return c
	case []interface{}:
		var sb strings.Builder
		for _, block := range c {
			if bm, ok := block.(map[string]interface{}); ok {
				if bm["type"] == "text" {
					if s, ok := bm["text"].(string); ok {
						sb.WriteString(s)
					}
				}
			}
		}
		return sb.String()
	}
	return ""
}

// buildUserMessageFromOpenAI builds a user message from OpenAI format and extracts tool results
func buildUserMessageFromOpenAI(msg gjson.Result, modelID, origin string) (KiroUserInputMessage, []KiroToolResult) {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolResults []KiroToolResult
	var images []KiroImage

	if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "image_url":
				if image, ok := openAIImageFromDataURL(part.Get("image_url.url").String()); ok {
					images = append(images, image)
				}
			}
		}
	} else if content.Type == gjson.String {
		contentBuilder.WriteString(content.String())
	}

	userMsg := KiroUserInputMessage{
		Content: contentBuilder.String(),
		ModelID: modelID,
		Origin:  origin,
	}

	if len(images) > 0 {
		userMsg.Images, _ = kirocommon.LimitKiroImages(images)
	}

	return userMsg, toolResults
}

func openAIImageFromDataURL(imageURL string) (KiroImage, bool) {
	if !strings.HasPrefix(imageURL, "data:") {
		return KiroImage{}, false
	}
	idx := strings.Index(imageURL, ";base64,")
	if idx < 0 {
		return KiroImage{}, false
	}
	mediaType := imageURL[5:idx]
	data := imageURL[idx+8:]
	lastSlash := strings.LastIndex(mediaType, "/")
	if lastSlash < 0 || lastSlash == len(mediaType)-1 || data == "" {
		return KiroImage{}, false
	}
	return KiroImage{
		Format: mediaType[lastSlash+1:],
		Source: KiroImageSource{Bytes: data},
	}, true
}

// buildAssistantMessageFromOpenAI builds an assistant message from OpenAI format
func buildAssistantMessageFromOpenAI(msg gjson.Result, allowStructuredTools bool) KiroAssistantResponseMessage {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolUses []KiroToolUse

	// Handle content
	if content.Type == gjson.String {
		contentBuilder.WriteString(content.String())
	} else if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "tool_use":
				// Handle tool_use in content array (Anthropic/OpenCode format)
				// This is different from OpenAI's tool_calls format
				toolUseID := part.Get("id").String()
				toolName := part.Get("name").String()
				inputData := part.Get("input")
				if !allowStructuredTools {
					contentBuilder.WriteString("\n[Tool call ")
					contentBuilder.WriteString(toolName)
					contentBuilder.WriteString(": ")
					contentBuilder.WriteString(inputData.Raw)
					contentBuilder.WriteString("]")
					continue
				}

				inputMap := make(map[string]interface{})
				if inputData.Exists() && inputData.IsObject() {
					inputData.ForEach(func(key, value gjson.Result) bool {
						inputMap[key.String()] = value.Value()
						return true
					})
				}

				toolUses = append(toolUses, KiroToolUse{
					ToolUseID: kirocommon.NormalizeKiroToolUseID(toolUseID),
					Name:      kirocommon.NormalizeKiroToolName(toolName),
					Input:     inputMap,
				})
				log.Debugf("kiro-openai: extracted tool_use from content array: %s", toolName)
			}
		}
	}

	// Handle tool_calls (OpenAI format)
	toolCalls := msg.Get("tool_calls")
	if toolCalls.IsArray() {
		for _, tc := range toolCalls.Array() {
			if tc.Get("type").String() != "function" {
				continue
			}

			toolUseID := tc.Get("id").String()
			toolName := tc.Get("function.name").String()
			toolArgs := tc.Get("function.arguments").String()
			if !allowStructuredTools {
				contentBuilder.WriteString("\n[Tool call ")
				contentBuilder.WriteString(toolName)
				contentBuilder.WriteString(": ")
				contentBuilder.WriteString(toolArgs)
				contentBuilder.WriteString("]")
				continue
			}

			var inputMap map[string]interface{}
			if err := json.Unmarshal([]byte(toolArgs), &inputMap); err != nil {
				log.Debugf("kiro-openai: failed to parse tool arguments: %v", err)
				inputMap = make(map[string]interface{})
			}

			toolUses = append(toolUses, KiroToolUse{
				ToolUseID: kirocommon.NormalizeKiroToolUseID(toolUseID),
				Name:      kirocommon.NormalizeKiroToolName(toolName),
				Input:     inputMap,
			})
		}
	}

	// CRITICAL FIX: Kiro API requires non-empty content for assistant messages
	// This can happen with compaction requests or error recovery scenarios
	finalContent := contentBuilder.String()
	if strings.TrimSpace(finalContent) == "" {
		if len(toolUses) > 0 {
			finalContent = kirocommon.DefaultAssistantContentWithTools
		} else {
			finalContent = kirocommon.DefaultAssistantContent
		}
		log.Debugf("kiro-openai: assistant content was empty, using default: %s", finalContent)
	}

	// Forward signed prior-turn reasoning through the nested reasoningContent
	// field (kiro-lb build_kiro_history). Kiro enforces the signature
	// (THINKING_SIGNATURE_INVALID on empty/fabricated values), so only a
	// client-supplied signature is forwarded; unsigned reasoning is dropped.
	reasoningText := msg.Get("reasoning_content").String()
	if reasoningText == "" {
		reasoningText = msg.Get("reasoning").String()
	}
	reasoningSignature := msg.Get("reasoning_signature").String()
	var reasoningContent *KiroReasoningContent
	if reasoningText != "" && reasoningSignature != "" {
		reasoningContent = &KiroReasoningContent{
			ReasoningText: KiroReasoningText{
				Text:      reasoningText,
				Signature: reasoningSignature,
			},
		}
	}

	return KiroAssistantResponseMessage{
		Content:          finalContent,
		ToolUses:         toolUses,
		ReasoningContent: reasoningContent,
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// buildFinalContent builds the final content with system prompt
func buildFinalContent(content, systemPrompt string, toolResults []KiroToolResult) string {
	var contentBuilder strings.Builder

	if systemPrompt != "" {
		contentBuilder.WriteString("--- SYSTEM PROMPT ---\n")
		contentBuilder.WriteString(systemPrompt)
		contentBuilder.WriteString("\n--- END SYSTEM PROMPT ---\n\n")
	}

	contentBuilder.WriteString(content)
	finalContent := contentBuilder.String()

	return finalContent
}

// checkThinkingModeFromOpenAI checks if thinking mode is enabled in the OpenAI request.
// Returns thinkingEnabled.
// Supports:
// - reasoning_effort parameter (low/medium/high/auto)
// - Model name containing "thinking" or "reason"
// - <thinking_mode> tag in system prompt (AMP/Cursor format)
func checkThinkingModeFromOpenAI(openaiBody []byte) bool {
	return checkThinkingModeFromOpenAIWithHeaders(openaiBody, nil)
}

// checkThinkingModeFromOpenAIWithHeaders checks if thinking mode is enabled in the OpenAI request.
// Returns thinkingEnabled.
// Supports:
// - Anthropic-Beta header with interleaved-thinking (Claude CLI)
// - reasoning_effort parameter (low/medium/high/auto)
// - Model name containing "thinking" or "reason"
// - <thinking_mode> tag in system prompt (AMP/Cursor format)
func checkThinkingModeFromOpenAIWithHeaders(openaiBody []byte, headers http.Header) bool {
	// Check Anthropic-Beta header first (Claude CLI uses this)
	if kiroclaude.IsThinkingEnabledFromHeader(headers) {
		log.Debugf("kiro-openai: thinking mode enabled via Anthropic-Beta header")
		return true
	}

	// Check OpenAI format: reasoning_effort parameter
	// Valid values: "low", "medium", "high", "auto" (not "none")
	reasoningEffort := gjson.GetBytes(openaiBody, "reasoning_effort")
	if reasoningEffort.Exists() {
		effort := reasoningEffort.String()
		if effort != "" && effort != "none" {
			log.Debugf("kiro-openai: thinking mode enabled via reasoning_effort: %s", effort)
			return true
		}
	}

	// Check AMP/Cursor format: <thinking_mode>interleaved</thinking_mode> in system prompt
	bodyStr := string(openaiBody)
	if strings.Contains(bodyStr, "<thinking_mode>") && strings.Contains(bodyStr, "</thinking_mode>") {
		startTag := "<thinking_mode>"
		endTag := "</thinking_mode>"
		startIdx := strings.Index(bodyStr, startTag)
		if startIdx >= 0 {
			startIdx += len(startTag)
			endIdx := strings.Index(bodyStr[startIdx:], endTag)
			if endIdx >= 0 {
				thinkingMode := bodyStr[startIdx : startIdx+endIdx]
				if thinkingMode == "interleaved" || thinkingMode == "enabled" {
					log.Debugf("kiro-openai: thinking mode enabled via AMP/Cursor format: %s", thinkingMode)
					return true
				}
			}
		}
	}

	// Check model name for thinking hints
	model := gjson.GetBytes(openaiBody, "model").String()
	modelLower := strings.ToLower(model)
	if strings.Contains(modelLower, "thinking") || strings.Contains(modelLower, "-reason") {
		log.Debugf("kiro-openai: thinking mode enabled via model name hint: %s", model)
		return true
	}

	log.Debugf("kiro-openai: no thinking mode detected in OpenAI request")
	return false
}

// hasThinkingTagInBody checks if the request body already contains thinking configuration tags.
// This is used to prevent duplicate injection when client (e.g., AMP/Cursor) already includes thinking config.
func hasThinkingTagInBody(body []byte) bool {
	bodyStr := string(body)
	return strings.Contains(bodyStr, "<thinking_mode>") || strings.Contains(bodyStr, "<max_thinking_length>")
}

// extractToolChoiceHint extracts tool_choice from OpenAI request and returns a system prompt hint.
// OpenAI tool_choice values:
// - "none": Don't use any tools
// - "auto": Model decides (default, no hint needed)
// - "required": Must use at least one tool
// - {"type":"function","function":{"name":"..."}} : Must use specific tool
func extractToolChoiceHint(openaiBody []byte) string {
	toolChoice := gjson.GetBytes(openaiBody, "tool_choice")
	if !toolChoice.Exists() {
		return ""
	}

	// Handle string values
	if toolChoice.Type == gjson.String {
		switch toolChoice.String() {
		case "none":
			// Note: When tool_choice is "none", we should ideally not pass tools at all
			// But since we can't modify tool passing here, we add a strong hint
			return "[INSTRUCTION: Do NOT use any tools. Respond with text only.]"
		case "required":
			return "[INSTRUCTION: You MUST use at least one of the available tools to respond. Do not respond with text only - always make a tool call.]"
		case "auto":
			// Default behavior, no hint needed
			return ""
		}
	}

	// Handle object value: {"type":"function","function":{"name":"..."}}
	if toolChoice.IsObject() {
		if toolChoice.Get("type").String() == "function" {
			toolName := toolChoice.Get("function.name").String()
			if toolName != "" {
				return fmt.Sprintf("[INSTRUCTION: You MUST use the tool named '%s' to respond. Do not use any other tool or respond with text only.]", toolName)
			}
		}
	}

	return ""
}

// extractResponseFormatHint extracts response_format from OpenAI request and returns a system prompt hint.
// OpenAI response_format values:
// - {"type": "text"}: Default, no hint needed
// - {"type": "json_object"}: Must respond with valid JSON
// - {"type": "json_schema", "json_schema": {...}}: Must respond with JSON matching schema
func extractResponseFormatHint(openaiBody []byte) string {
	responseFormat := gjson.GetBytes(openaiBody, "response_format")
	if !responseFormat.Exists() {
		return ""
	}

	formatType := responseFormat.Get("type").String()
	switch formatType {
	case "json_object":
		return "[INSTRUCTION: You MUST respond with valid JSON only. Do not include any text before or after the JSON. Do not wrap the JSON in markdown code blocks. Output raw JSON directly.]"
	case "json_schema":
		// Extract schema if provided
		schema := responseFormat.Get("json_schema.schema")
		if schema.Exists() {
			schemaStr := schema.Raw
			// Truncate if too long
			if len(schemaStr) > 500 {
				schemaStr = schemaStr[:500] + "..."
			}
			return fmt.Sprintf("[INSTRUCTION: You MUST respond with valid JSON that matches this schema: %s. Do not include any text before or after the JSON. Do not wrap the JSON in markdown code blocks. Output raw JSON directly.]", schemaStr)
		}
		return "[INSTRUCTION: You MUST respond with valid JSON only. Do not include any text before or after the JSON. Do not wrap the JSON in markdown code blocks. Output raw JSON directly.]"
	case "text":
		// Default behavior, no hint needed
		return ""
	}

	return ""
}

// deduplicateToolResults removes duplicate tool results
func deduplicateToolResults(toolResults []KiroToolResult) []KiroToolResult {
	if len(toolResults) == 0 {
		return toolResults
	}

	seenIDs := make(map[string]bool)
	unique := make([]KiroToolResult, 0, len(toolResults))
	for _, tr := range toolResults {
		if !seenIDs[tr.ToolUseID] {
			seenIDs[tr.ToolUseID] = true
			unique = append(unique, tr)
		} else {
			log.Debugf("kiro-openai: skipping duplicate toolResult: %s", tr.ToolUseID)
		}
	}
	return unique
}

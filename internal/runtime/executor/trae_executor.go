package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type ContextResolver struct {
	ResolverID string `json:"resolver_id"`
	Variables  string `json:"variables"`
}

type LastLLMResponseInfo struct {
	Turn     int    `json:"turn"`
	IsError  bool   `json:"is_error"`
	Response string `json:"response"`
}

type TraeRequest struct {
	UserInput                  string               `json:"user_input"`
	IntentName                 string               `json:"intent_name"`
	Variables                  string               `json:"variables"`
	ContextResolvers           []ContextResolver    `json:"context_resolvers"`
	GenerateSuggestedQuestions bool                 `json:"generate_suggested_questions"`
	ChatHistory                []ChatHistory        `json:"chat_history"`
	SessionID                  string               `json:"session_id"`
	ConversationID             string               `json:"conversation_id"`
	CurrentTurn                int                  `json:"current_turn"`
	ValidTurns                 []int                `json:"valid_turns"`
	MultiMedia                 []interface{}        `json:"multi_media"`
	ModelName                  string               `json:"model_name"`
	LastLLMResponseInfo        *LastLLMResponseInfo `json:"last_llm_response_info,omitempty"`
	IsPreset                   bool                 `json:"is_preset"`
	Provider                   string               `json:"provider"`
}

type ChatHistory struct {
	Role      string `json:"role"`
	SessionID string `json:"session_id"`
	Locale    string `json:"locale"`
	Content   string `json:"content"`
	Status    string `json:"status"`
}

type OpenAIMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type OpenAIRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type GetDetailParamRequest struct {
	Function   string `json:"function"`
	NeedPrompt bool   `json:"need_prompt"`
	PolyPrompt bool   `json:"poly_prompt"`
}

type GetDetailParamResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		ConfigInfoList []struct {
			Function        string `json:"function"`
			ModelDetailList []struct {
				ModelName            string   `json:"model_name"`
				EncryptedModelParams string   `json:"encrypted_model_params"`
				DisplayName          string   `json:"display_name"`
				Tags                 []string `json:"tags"`
			} `json:"model_detail_list"`
		} `json:"config_info_list"`
	} `json:"data"`
}

type TraeV3Request struct {
	EncryptedModelParams string                 `json:"encrypted_model_params"`
	Model                string                 `json:"model"`
	Messages             []TraeV3Message        `json:"messages"`
	Stream               bool                   `json:"stream"`
	MaxTokens            int                    `json:"max_tokens,omitempty"`
	Temperature          float64                `json:"temperature,omitempty"`
	AgentTaskContext     map[string]interface{} `json:"agent_task_context"`
}

type TraeV3Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type TraeExecutor struct {
	cfg *config.Config
}

func NewTraeExecutor(cfg *config.Config) *TraeExecutor {
	return &TraeExecutor{cfg: cfg}
}

func convertModelName(model string) string {
	// Known valid Trae models:
	// - gpt-5-2-codex
	// - gpt-4o
	// - deepseek-V3
	// - deepseek-R1
	// - aws_sdk_claude37_sonnet

	switch model {
	case "claude-3-5-sonnet-20240620", "claude-3-5-sonnet-20241022", "claude-3-5-sonnet":
		return model // Return as is, "claude3.5" is invalid
	case "claude-3-7-sonnet-20250219", "claude-3-7-sonnet", "claude-3-7":
		return "aws_sdk_claude37_sonnet"
	case "gpt-4o-mini", "gpt-4o-mini-2024-07-18", "gpt-4o-latest":
		return "gpt-4o"
	case "deepseek-chat", "deepseek-coder", "deepseek-v3":
		return "deepseek-V3"
	case "deepseek-reasoner", "deepseek-r1":
		return "deepseek-R1"
	default:
		return model
	}
}

// isV3Model checks if the model requires v3 API (builder_v3)
// These models are only available through the v3 agent API endpoint
func isV3Model(model string) bool {
	v3Models := map[string]bool{
		// GPT-5 family
		"gpt-5": true, "gpt-5.1": true, "gpt-5.2": true, "gpt-5-medium": true, "gpt-5.2-codex": true,
		"gpt-5-high": true, "gpt-5-mini": true,
		// Gemini 3 family
		"gemini-3-pro": true, "gemini-3-flash": true, "gemini-3-pro-200k": true, "gemini-3-flash-solo": true,
		// Kimi K2
		"kimi-k2": true, "kimi-k2-0905": true,
		// DeepSeek V3.1
		"deepseek-v3.1": true,
	}
	return v3Models[model]
}

func (e *TraeExecutor) getEncryptedModelParams(ctx context.Context, accessToken, host, appID, modelName string) (string, error) {
	reqBody := GetDetailParamRequest{
		Function:   "builder_v3",
		NeedPrompt: false,
		PolyPrompt: true,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("trae: failed to marshal get_detail_param request: %w", err)
	}

	url := fmt.Sprintf("%s/api/ide/v1/get_detail_param", host)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}

	deviceID, machineID, deviceBrand := generateDeviceInfo(extractUserIDFromToken(accessToken))

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-app-id", appID)
	httpReq.Header.Set("x-ide-version", "3.5.25")
	httpReq.Header.Set("x-ide-version-code", "20260120")
	httpReq.Header.Set("x-ide-version-type", "stable")
	httpReq.Header.Set("x-device-cpu", "Intel")
	httpReq.Header.Set("x-device-id", deviceID)
	httpReq.Header.Set("x-machine-id", machineID)
	httpReq.Header.Set("x-device-brand", deviceBrand)
	httpReq.Header.Set("x-device-type", "mac")
	httpReq.Header.Set("x-os-version", "macOS 15.7.3")
	httpReq.Header.Set("x-ide-token", accessToken)
	httpReq.Header.Set("User-Agent", "TraeClient/TTNet")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("trae: get_detail_param request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("trae: failed to read get_detail_param response: %w", err)
	}

	var paramResp GetDetailParamResponse
	if err := json.Unmarshal(body, &paramResp); err != nil {
		return "", fmt.Errorf("trae: failed to parse get_detail_param response: %w", err)
	}

	if paramResp.Code != 0 {
		return "", fmt.Errorf("trae: get_detail_param failed: %s", paramResp.Message)
	}

	for _, configInfo := range paramResp.Data.ConfigInfoList {
		if configInfo.Function == "builder_v3" {
			for _, modelDetail := range configInfo.ModelDetailList {
				if modelDetail.ModelName == modelName {
					return modelDetail.EncryptedModelParams, nil
				}
			}
		}
	}

	return "", fmt.Errorf("trae: model '%s' not found in get_detail_param response", modelName)
}

func extractUserIDFromToken(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Data.ID
}

func generateDeviceInfo(userID string) (deviceID, machineID, deviceBrand string) {
	if userID != "" {
		deviceID = userID
	} else {
		deviceID = fmt.Sprintf("%d", rand.Int63())
	}

	bytes := make([]byte, 32)
	for i := range bytes {
		bytes[i] = byte(rand.Intn(16))
	}
	machineID = fmt.Sprintf("%x", bytes)

	brands := []string{"92L3", "91C9", "814S", "8P15V", "35G4"}
	deviceBrand = brands[rand.Intn(len(brands))]
	return
}

func generateSessionIDFromMessages(messages []OpenAIMessage) string {
	var conversationKey strings.Builder
	for _, msg := range messages[:1] {
		conversationKey.WriteString(msg.Role)
		conversationKey.WriteString(": ")
		conversationKey.WriteString(fmt.Sprintf("%v", msg.Content))
		conversationKey.WriteString("\n")
	}

	h := sha256.New()
	h.Write([]byte(conversationKey.String()))
	cacheKey := fmt.Sprintf("%x", h.Sum(nil))

	return cacheKey
}

func convertOpenAIToTrae(openAIReq *OpenAIRequest, userID string) (*TraeRequest, error) {
	if len(openAIReq.Messages) == 0 {
		return nil, fmt.Errorf("no messages provided")
	}

	sessionID := generateSessionIDFromMessages(openAIReq.Messages)
	deviceID, machineID, deviceBrand := generateDeviceInfo(userID)

	contextResolvers := []ContextResolver{
		{
			ResolverID: "project-labels",
			Variables:  "{\"labels\":\"- go\\n- go.mod\"}",
		},
		{
			ResolverID: "terminal_context",
			Variables:  "{\"terminal_context\":[]}",
		},
	}

	lastContent := fmt.Sprintf("%v", openAIReq.Messages[len(openAIReq.Messages)-1].Content)

	variablesJSON := map[string]interface{}{
		"language":                   "",
		"locale":                     "zh-cn",
		"input":                      lastContent,
		"version_code":               20250325,
		"is_inline_chat":             false,
		"is_command":                 false,
		"raw_input":                  lastContent,
		"problem":                    "",
		"current_filename":           "",
		"is_select_code_before_chat": false,
		"last_select_time":           int64(0),
		"last_turn_session":          "",
		"hash_workspace":             false,
		"hash_file":                  0,
		"hash_code":                  0,
		"use_filepath":               true,
		"current_time":               time.Now().Format("20060102 15:04:05，星期二"),
		"badge_clickable":            true,
		"workspace_path":             "/home/user/workspace/project",
		"brand":                      deviceBrand,
		"system_type":                "Windows",
		"device_id":                  deviceID,
		"machine_id":                 machineID,
	}

	variablesStr, err := json.Marshal(variablesJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal variables: %w", err)
	}

	chatHistory := make([]ChatHistory, 0)
	for _, msg := range openAIReq.Messages[:len(openAIReq.Messages)-1] {
		var locale string
		if msg.Role == "assistant" {
			locale = "zh-cn"
		}

		chatHistory = append(chatHistory, ChatHistory{
			Role:      msg.Role,
			Content:   fmt.Sprintf("%v", msg.Content),
			Status:    "success",
			Locale:    locale,
			SessionID: sessionID,
		})
	}

	var lastLLMResponseInfo *LastLLMResponseInfo
	if len(chatHistory) > 0 {
		lastMsg := chatHistory[len(chatHistory)-1]
		if lastMsg.Role == "assistant" {
			lastLLMResponseInfo = &LastLLMResponseInfo{
				Turn:     len(chatHistory) - 1,
				IsError:  false,
				Response: lastMsg.Content,
			}
		}
	}

	validTurns := make([]int, len(chatHistory))
	for i := range validTurns {
		validTurns[i] = i
	}

	return &TraeRequest{
		UserInput:                  lastContent,
		IntentName:                 "general_qa_intent",
		Variables:                  string(variablesStr),
		ContextResolvers:           contextResolvers,
		GenerateSuggestedQuestions: false,
		ChatHistory:                chatHistory,
		SessionID:                  sessionID,
		ConversationID:             sessionID,
		CurrentTurn:                len(openAIReq.Messages) - 1,
		ValidTurns:                 validTurns,
		MultiMedia:                 []interface{}{},
		ModelName:                  convertModelName(openAIReq.Model),
		LastLLMResponseInfo:        lastLLMResponseInfo,
		IsPreset:                   true,
		Provider:                   "",
	}, nil
}

func (e *TraeExecutor) Provider() string {
	return "trae"
}

func (e *TraeExecutor) Identifier() string {
	return "trae"
}

// traeCreds extracts access token and host from auth metadata.
// Supports both "token" and "access_token" field names for compatibility.
func traeCreds(auth *coreauth.Auth) (accessToken, host, appID string) {
	// Default to v1 API host discovered from MITM analysis
	host = "https://api22-normal-alisg.mchost.guru"
	appID = "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8"
	if auth == nil || auth.Metadata == nil {
		return "", host, appID
	}
	// Check "access_token" first, then fall back to "token"
	if v, ok := auth.Metadata["access_token"].(string); ok && v != "" {
		accessToken = v
	} else if v, ok := auth.Metadata["token"].(string); ok && v != "" {
		accessToken = v
	}
	if v, ok := auth.Metadata["host"].(string); ok && v != "" {
		host = v
	}
	if v, ok := auth.Metadata["app_id"].(string); ok && v != "" {
		appID = v
	}
	return accessToken, host, appID
}

func (e *TraeExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := req.Model

	accessToken, host, appID := traeCreds(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("trae: missing access token")
	}

	if isV3Model(baseModel) {
		return e.executeV3(ctx, auth, req, opts, accessToken, host, appID)
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	var openAIReq OpenAIRequest
	if err := json.Unmarshal(req.Payload, &openAIReq); err != nil {
		return resp, fmt.Errorf("trae: failed to parse OpenAI request: %w", err)
	}

	traeReq, err := convertOpenAIToTrae(&openAIReq, extractUserIDFromToken(accessToken))
	if err != nil {
		return resp, fmt.Errorf("trae: failed to convert request: %w", err)
	}

	jsonData, err := json.Marshal(traeReq)
	if err != nil {
		return resp, fmt.Errorf("trae: failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/ide/v1/chat", host)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return resp, err
	}

	deviceID, machineID, deviceBrand := generateDeviceInfo(extractUserIDFromToken(accessToken))

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-app-id", appID)
	httpReq.Header.Set("x-ide-version", "3.5.25")
	httpReq.Header.Set("x-ide-version-code", "20260120")
	httpReq.Header.Set("x-ide-version-type", "stable")
	httpReq.Header.Set("x-device-cpu", "Intel")
	httpReq.Header.Set("x-device-id", deviceID)
	httpReq.Header.Set("x-machine-id", machineID)
	httpReq.Header.Set("x-device-brand", deviceBrand)
	httpReq.Header.Set("x-device-type", "mac")
	httpReq.Header.Set("x-os-version", "macOS 15.7.3")
	httpReq.Header.Set("x-ide-token", accessToken)
	httpReq.Header.Set("x-ahanet-timeout", "86400")
	httpReq.Header.Set("User-Agent", "TraeClient/TTNet")
	httpReq.Header.Set("accept", "*/*")
	httpReq.Header.Set("Connection", "keep-alive")

	if auth != nil && auth.Attributes != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}

	var authID string
	if auth != nil {
		authID = auth.ID
	}

	log.WithFields(log.Fields{
		"auth_id":  authID,
		"provider": e.Identifier(),
		"model":    baseModel,
		"url":      url,
		"method":   http.MethodPost,
	}).Infof("external HTTP request: POST %s", url)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return resp, fmt.Errorf("trae: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(httpResp.Body)
		return resp, fmt.Errorf("trae: API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	var fullResponse string
	var lastFinishReason string
	var promptTokens, completionTokens, totalTokens int
	reader := bufio.NewReader(httpResp.Body)

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return resp, fmt.Errorf("trae: failed to read response: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			event := strings.TrimPrefix(line, "event: ")
			dataLine, err := reader.ReadString('\n')
			if err != nil {
				continue
			}
			dataLine = strings.TrimSpace(dataLine)
			if !strings.HasPrefix(dataLine, "data: ") {
				continue
			}
			data := strings.TrimPrefix(dataLine, "data: ")

			switch event {
			case "output":
				var outputData struct {
					Response         string `json:"response"`
					ReasoningContent string `json:"reasoning_content"`
					FinishReason     string `json:"finish_reason"`
				}
				if err := json.Unmarshal([]byte(data), &outputData); err != nil {
					continue
				}

				if outputData.Response != "" {
					fullResponse += outputData.Response
				}
				if outputData.ReasoningContent != "" {
					fullResponse += outputData.ReasoningContent
				}
				if outputData.FinishReason != "" {
					lastFinishReason = outputData.FinishReason
				}

			case "token_usage":
				var usageData struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				}
				if err := json.Unmarshal([]byte(data), &usageData); err == nil {
					promptTokens = usageData.PromptTokens
					completionTokens = usageData.CompletionTokens
					totalTokens = usageData.TotalTokens
				}

			case "done":
				var doneData struct {
					FinishReason string `json:"finish_reason"`
				}
				if err := json.Unmarshal([]byte(data), &doneData); err == nil && doneData.FinishReason != "" {
					lastFinishReason = doneData.FinishReason
				}
			}
		}
	}

	if lastFinishReason == "" {
		lastFinishReason = "stop"
	}

	openAIResponse := map[string]interface{}{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   baseModel,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": fullResponse,
				},
				"finish_reason": lastFinishReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      totalTokens,
		},
	}

	responseBytes, err := json.Marshal(openAIResponse)
	if err != nil {
		return resp, fmt.Errorf("trae: failed to marshal response: %w", err)
	}

	return cliproxyexecutor.Response{Payload: responseBytes}, nil
}

func (e *TraeExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	baseModel := req.Model

	accessToken, host, appID := traeCreds(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("trae: missing access token")
	}

	if isV3Model(baseModel) {
		return e.executeStreamV3(ctx, auth, req, opts, accessToken, host, appID)
	}

	var openAIReq OpenAIRequest
	if err := json.Unmarshal(req.Payload, &openAIReq); err != nil {
		return nil, fmt.Errorf("trae: failed to parse OpenAI request: %w", err)
	}

	traeReq, err := convertOpenAIToTrae(&openAIReq, extractUserIDFromToken(accessToken))
	if err != nil {
		return nil, fmt.Errorf("trae: failed to convert request: %w", err)
	}

	jsonData, err := json.Marshal(traeReq)
	if err != nil {
		return nil, fmt.Errorf("trae: failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/ide/v1/chat", host)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}

	deviceID, machineID, deviceBrand := generateDeviceInfo(extractUserIDFromToken(accessToken))

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-app-id", appID)
	httpReq.Header.Set("x-ide-version", "3.5.25")
	httpReq.Header.Set("x-ide-version-code", "20260120")
	httpReq.Header.Set("x-ide-version-type", "stable")
	httpReq.Header.Set("x-device-cpu", "Intel")
	httpReq.Header.Set("x-device-id", deviceID)
	httpReq.Header.Set("x-machine-id", machineID)
	httpReq.Header.Set("x-device-brand", deviceBrand)
	httpReq.Header.Set("x-device-type", "mac")
	httpReq.Header.Set("x-os-version", "macOS 15.7.3")
	httpReq.Header.Set("x-ide-token", accessToken)
	httpReq.Header.Set("x-ahanet-timeout", "86400")
	httpReq.Header.Set("User-Agent", "TraeClient/TTNet")
	httpReq.Header.Set("accept", "*/*")
	httpReq.Header.Set("Connection", "keep-alive")

	if auth != nil && auth.Attributes != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}

	var authID string
	if auth != nil {
		authID = auth.ID
	}

	log.WithFields(log.Fields{
		"auth_id":  authID,
		"provider": e.Identifier(),
		"model":    baseModel,
		"url":      url,
		"method":   http.MethodPost,
	}).Infof("external HTTP stream request: POST %s", url)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("trae: stream request failed: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, fmt.Errorf("trae: API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	ch := make(chan cliproxyexecutor.StreamChunk, 100)

	go func() {
		defer close(ch)
		defer httpResp.Body.Close()

		reader := bufio.NewReader(httpResp.Body)
		var thinkStartType, thinkEndType bool

		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				break
			}
			if err != nil {
				ch <- cliproxyexecutor.StreamChunk{Err: err}
				return
			}

			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			if strings.HasPrefix(line, "event: ") {
				event := strings.TrimPrefix(line, "event: ")
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					continue
				}
				dataLine = strings.TrimSpace(dataLine)
				if !strings.HasPrefix(dataLine, "data: ") {
					continue
				}
				data := strings.TrimPrefix(dataLine, "data: ")

				switch event {
				case "output":
					var outputData struct {
						Response         string `json:"response"`
						ReasoningContent string `json:"reasoning_content"`
						FinishReason     string `json:"finish_reason"`
					}
					if err := json.Unmarshal([]byte(data), &outputData); err != nil {
						continue
					}

					var deltaContent string
					if outputData.ReasoningContent != "" {
						if !thinkStartType {
							deltaContent = "<think>\n\n" + outputData.ReasoningContent
							thinkStartType = true
							thinkEndType = false
						} else {
							deltaContent = outputData.ReasoningContent
						}
					}

					if outputData.Response != "" {
						if thinkStartType && !thinkEndType {
							deltaContent = "</think>\n\n" + outputData.Response
							thinkStartType = false
							thinkEndType = true
						} else {
							deltaContent = outputData.Response
						}
					}

					if deltaContent != "" {
						openAIResponse := map[string]interface{}{
							"id":      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
							"object":  "chat.completion.chunk",
							"created": time.Now().Unix(),
							"model":   baseModel,
							"choices": []map[string]interface{}{
								{
									"index": 0,
									"delta": map[string]interface{}{
										"content": deltaContent,
									},
									"finish_reason": nil,
								},
							},
						}
						responseJSON, _ := json.Marshal(openAIResponse)
						ch <- cliproxyexecutor.StreamChunk{
							Payload: append([]byte("data: "), append(responseJSON, []byte("\n\n")...)...),
							Err:     nil,
						}
					}

				case "thought":
					var thoughtData struct {
						Thought          string `json:"thought"`
						ReasoningContent string `json:"reasoning_content"`
					}
					if err := json.Unmarshal([]byte(data), &thoughtData); err != nil {
						continue
					}

					content := thoughtData.Thought
					if content == "" {
						content = thoughtData.ReasoningContent
					}

					if content != "" {
						openAIResponse := map[string]interface{}{
							"id":      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
							"object":  "chat.completion.chunk",
							"created": time.Now().Unix(),
							"model":   baseModel,
							"choices": []map[string]interface{}{
								{
									"index": 0,
									"delta": map[string]interface{}{
										"content": content,
									},
									"finish_reason": nil,
								},
							},
						}
						responseJSON, _ := json.Marshal(openAIResponse)
						ch <- cliproxyexecutor.StreamChunk{
							Payload: append([]byte("data: "), append(responseJSON, []byte("\n\n")...)...),
							Err:     nil,
						}
					}

				case "turn_completion", "done":
					var doneData struct {
						FinishReason string `json:"finish_reason"`
					}
					finishReason := "stop"
					if err := json.Unmarshal([]byte(data), &doneData); err == nil && doneData.FinishReason != "" {
						finishReason = doneData.FinishReason
					}

					openAIResponse := map[string]interface{}{
						"id":      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
						"object":  "chat.completion.chunk",
						"created": time.Now().Unix(),
						"model":   baseModel,
						"choices": []map[string]interface{}{
							{
								"index":         0,
								"delta":         map[string]interface{}{},
								"finish_reason": finishReason,
							},
						},
					}
					responseJSON, _ := json.Marshal(openAIResponse)
					ch <- cliproxyexecutor.StreamChunk{
						Payload: append([]byte("data: "), append(responseJSON, []byte("\n\n")...)...),
						Err:     nil,
					}
					ch <- cliproxyexecutor.StreamChunk{
						Payload: []byte("data: [DONE]\n\n"),
						Err:     nil,
					}
					return
				}
			}
		}
	}()

	return ch, nil
}

func (e *TraeExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("trae: CountTokens not implemented")
}

func (e *TraeExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("trae executor: auth is nil")
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" && auth.Attributes != nil {
		refreshToken = auth.Attributes["refresh_token"]
	}
	if refreshToken == "" {
		return auth, nil
	}

	return auth, fmt.Errorf("trae: token refresh not implemented")
}

func (e *TraeExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("trae executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}

	httpReq := req.WithContext(ctx)

	accessToken := ""
	if auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok && v != "" {
			accessToken = v
		}
	}

	if accessToken == "" && auth != nil && auth.Attributes != nil {
		if v, ok := auth.Attributes["access_token"]; ok && v != "" {
			accessToken = v
		}
	}

	if accessToken == "" {
		return nil, fmt.Errorf("trae executor: missing access token in auth metadata or attributes")
	}

	httpReq.Header.Set("Authorization", "Bearer "+accessToken)

	if auth != nil && auth.Attributes != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *TraeExecutor) executeV3(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, host, appID string) (resp cliproxyexecutor.Response, err error) {
	baseModel := req.Model

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	var openAIReq OpenAIRequest
	if err := json.Unmarshal(req.Payload, &openAIReq); err != nil {
		return resp, fmt.Errorf("trae: failed to parse OpenAI request: %w", err)
	}

	encryptedParams, err := e.getEncryptedModelParams(ctx, accessToken, host, appID, baseModel)
	if err != nil {
		return resp, err
	}

	var messages []TraeV3Message
	for _, msg := range openAIReq.Messages {
		content := ""
		switch c := msg.Content.(type) {
		case string:
			content = c
		case []interface{}:
			for _, part := range c {
				if m, ok := part.(map[string]interface{}); ok {
					if text, ok := m["text"].(string); ok {
						content += text
					}
				}
			}
		}
		messages = append(messages, TraeV3Message{
			Role:    msg.Role,
			Content: content,
		})
	}

	v3Req := TraeV3Request{
		EncryptedModelParams: encryptedParams,
		Model:                baseModel,
		Messages:             messages,
		Stream:               false,
		AgentTaskContext:     map[string]interface{}{},
	}

	jsonData, err := json.Marshal(v3Req)
	if err != nil {
		return resp, fmt.Errorf("trae: failed to marshal v3 request: %w", err)
	}

	v3Host := "https://coresg-normal.trae.ai"
	url := fmt.Sprintf("%s/api/agent/v3/create_agent_task", v3Host)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return resp, err
	}

	deviceID, machineID, deviceBrand := generateDeviceInfo(extractUserIDFromToken(accessToken))

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-app-id", appID)
	httpReq.Header.Set("x-ide-version", "3.5.25")
	httpReq.Header.Set("x-ide-version-code", "20260120")
	httpReq.Header.Set("x-ide-version-type", "stable")
	httpReq.Header.Set("x-device-cpu", "Intel")
	httpReq.Header.Set("x-device-id", deviceID)
	httpReq.Header.Set("x-machine-id", machineID)
	httpReq.Header.Set("x-device-brand", deviceBrand)
	httpReq.Header.Set("x-device-type", "mac")
	httpReq.Header.Set("x-os-version", "macOS 15.7.3")
	httpReq.Header.Set("x-ide-token", accessToken)
	httpReq.Header.Set("User-Agent", "TraeClient/TTNet")

	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	log.WithFields(log.Fields{
		"auth_id":  authID,
		"provider": e.Identifier(),
		"model":    baseModel,
		"url":      url,
		"method":   http.MethodPost,
	}).Infof("external HTTP request (v3): POST %s", url)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return resp, fmt.Errorf("trae: v3 request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(httpResp.Body)
		return resp, fmt.Errorf("trae: v3 API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	var fullResponse string
	reader := bufio.NewReader(httpResp.Body)

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return resp, fmt.Errorf("trae: error reading v3 response: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			fullResponse += choice.Delta.Content
		}
	}

	openAIResp := map[string]interface{}{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   baseModel,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": fullResponse,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}

	respJSON, err := json.Marshal(openAIResp)
	if err != nil {
		return resp, fmt.Errorf("trae: failed to marshal response: %w", err)
	}

	resp.Payload = respJSON
	return resp, nil
}

func (e *TraeExecutor) executeStreamV3(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, host, appID string) (<-chan cliproxyexecutor.StreamChunk, error) {
	baseModel := req.Model

	var openAIReq OpenAIRequest
	if err := json.Unmarshal(req.Payload, &openAIReq); err != nil {
		return nil, fmt.Errorf("trae: failed to parse OpenAI request: %w", err)
	}

	encryptedParams, err := e.getEncryptedModelParams(ctx, accessToken, host, appID, baseModel)
	if err != nil {
		return nil, err
	}

	var messages []TraeV3Message
	for _, msg := range openAIReq.Messages {
		content := ""
		switch c := msg.Content.(type) {
		case string:
			content = c
		case []interface{}:
			for _, part := range c {
				if m, ok := part.(map[string]interface{}); ok {
					if text, ok := m["text"].(string); ok {
						content += text
					}
				}
			}
		}
		messages = append(messages, TraeV3Message{
			Role:    msg.Role,
			Content: content,
		})
	}

	v3Req := TraeV3Request{
		EncryptedModelParams: encryptedParams,
		Model:                baseModel,
		Messages:             messages,
		Stream:               true,
		AgentTaskContext:     map[string]interface{}{},
	}

	jsonData, err := json.Marshal(v3Req)
	if err != nil {
		return nil, fmt.Errorf("trae: failed to marshal v3 request: %w", err)
	}

	v3Host := "https://coresg-normal.trae.ai"
	url := fmt.Sprintf("%s/api/agent/v3/create_agent_task", v3Host)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}

	deviceID, machineID, deviceBrand := generateDeviceInfo(extractUserIDFromToken(accessToken))

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-app-id", appID)
	httpReq.Header.Set("x-ide-version", "3.5.25")
	httpReq.Header.Set("x-ide-version-code", "20260120")
	httpReq.Header.Set("x-ide-version-type", "stable")
	httpReq.Header.Set("x-device-cpu", "Intel")
	httpReq.Header.Set("x-device-id", deviceID)
	httpReq.Header.Set("x-machine-id", machineID)
	httpReq.Header.Set("x-device-brand", deviceBrand)
	httpReq.Header.Set("x-device-type", "mac")
	httpReq.Header.Set("x-os-version", "macOS 15.7.3")
	httpReq.Header.Set("x-ide-token", accessToken)
	httpReq.Header.Set("User-Agent", "TraeClient/TTNet")

	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	log.WithFields(log.Fields{
		"auth_id":  authID,
		"provider": e.Identifier(),
		"model":    baseModel,
		"url":      url,
		"method":   http.MethodPost,
	}).Infof("external HTTP stream request (v3): POST %s", url)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("trae: v3 stream request failed: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, fmt.Errorf("trae: v3 stream API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	chunkChan := make(chan cliproxyexecutor.StreamChunk, 100)

	go func() {
		defer close(chunkChan)
		defer httpResp.Body.Close()

		reader := bufio.NewReader(httpResp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				break
			}
			if err != nil {
				chunkChan <- cliproxyexecutor.StreamChunk{Err: err}
				return
			}

			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}

			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				doneChunk := map[string]interface{}{
					"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   baseModel,
					"choices": []map[string]interface{}{
						{
							"index":         0,
							"delta":         map[string]interface{}{},
							"finish_reason": "stop",
						},
					},
				}
				doneJSON, _ := json.Marshal(doneChunk)
				chunkChan <- cliproxyexecutor.StreamChunk{Payload: []byte("data: " + string(doneJSON) + "\n\n")}
				chunkChan <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
				break
			}

			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			for _, choice := range chunk.Choices {
				if choice.Delta.Content == "" {
					continue
				}
				openAIChunk := map[string]interface{}{
					"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   baseModel,
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]interface{}{
								"content": choice.Delta.Content,
							},
							"finish_reason": nil,
						},
					},
				}
				chunkJSON, _ := json.Marshal(openAIChunk)
				chunkChan <- cliproxyexecutor.StreamChunk{Payload: []byte("data: " + string(chunkJSON) + "\n\n")}
			}
		}
	}()

	return chunkChan, nil
}

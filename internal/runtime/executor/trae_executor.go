package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
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

type TraeExecutor struct {
	cfg *config.Config
}

func NewTraeExecutor(cfg *config.Config) *TraeExecutor {
	return &TraeExecutor{cfg: cfg}
}

func convertModelName(model string) string {
	switch model {
	case "claude-3-5-sonnet-20240620", "claude-3-5-sonnet-20241022", "claude-3-5-sonnet":
		return "claude3.5"
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

func generateDeviceInfo() (deviceID, machineID, deviceBrand string) {
	deviceID = fmt.Sprintf("%d", rand.Int63())

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

func convertOpenAIToTrae(openAIReq *OpenAIRequest) (*TraeRequest, error) {
	if len(openAIReq.Messages) == 0 {
		return nil, fmt.Errorf("no messages provided")
	}

	sessionID := generateSessionIDFromMessages(openAIReq.Messages)
	deviceID, machineID, deviceBrand := generateDeviceInfo()

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
func traeCreds(auth *coreauth.Auth) (accessToken, host, appID string) {
	host = "https://trae-api-sg.mchost.guru"
	appID = "trae_ide"
	if auth == nil || auth.Metadata == nil {
		return "", host, appID
	}
	if v, ok := auth.Metadata["access_token"].(string); ok && v != "" {
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

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	var openAIReq OpenAIRequest
	if err := json.Unmarshal(req.Payload, &openAIReq); err != nil {
		return resp, fmt.Errorf("trae: failed to parse OpenAI request: %w", err)
	}

	traeReq, err := convertOpenAIToTrae(&openAIReq)
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

	deviceID, machineID, deviceBrand := generateDeviceInfo()

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-app-id", appID)
	httpReq.Header.Set("x-ide-version", "1.2.10")
	httpReq.Header.Set("x-ide-version-code", "20250325")
	httpReq.Header.Set("x-ide-version-type", "stable")
	httpReq.Header.Set("x-device-cpu", "AMD")
	httpReq.Header.Set("x-device-id", deviceID)
	httpReq.Header.Set("x-machine-id", machineID)
	httpReq.Header.Set("x-device-brand", deviceBrand)
	httpReq.Header.Set("x-device-type", "windows")
	httpReq.Header.Set("x-ide-token", accessToken)
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
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
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

	var openAIReq OpenAIRequest
	if err := json.Unmarshal(req.Payload, &openAIReq); err != nil {
		return nil, fmt.Errorf("trae: failed to parse OpenAI request: %w", err)
	}

	traeReq, err := convertOpenAIToTrae(&openAIReq)
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

	deviceID, machineID, deviceBrand := generateDeviceInfo()

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-app-id", appID)
	httpReq.Header.Set("x-ide-version", "1.2.10")
	httpReq.Header.Set("x-ide-version-code", "20250325")
	httpReq.Header.Set("x-ide-version-type", "stable")
	httpReq.Header.Set("x-device-cpu", "AMD")
	httpReq.Header.Set("x-device-id", deviceID)
	httpReq.Header.Set("x-machine-id", machineID)
	httpReq.Header.Set("x-device-brand", deviceBrand)
	httpReq.Header.Set("x-device-type", "windows")
	httpReq.Header.Set("x-ide-token", accessToken)
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

				case "done":
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

package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/http2"
)

const (
	// Match senpi's current Cursor client for both AgentService and OAuth.
	cursorAgentURL          = "https://api2.cursor.sh"
	cursorAgentHost         = "api2.cursor.sh"
	cursorRunPath           = "/agent.v1.AgentService/Run"
	cursorModelsPath        = "/agent.v1.AgentService/GetUsableModels"
	cursorClientVersion     = "cli-2026.07.23-e383d2b"
	cursorAuthType          = "cursor"
	cursorHeartbeatInterval = 5 * time.Second
	cursorSessionTTL        = 5 * time.Minute
	cursorCheckpointTTL     = 30 * time.Minute
)

// CursorExecutor handles requests to the Cursor API via Connect+Protobuf protocol.
type CursorExecutor struct {
	cfg         *config.Config
	mu          sync.Mutex
	sessions    map[string]*cursorSession
	checkpoints map[string]*savedCheckpoint // keyed by conversationId
	rotations   map[string]string           // base conversationId -> one rotated wire conversationId
}

// savedCheckpoint stores the server's conversation_checkpoint_update for reuse.
type savedCheckpoint struct {
	data      []byte            // raw ConversationStateStructure protobuf bytes
	blobStore map[string][]byte // blobs referenced by the checkpoint
	authID    string            // auth that produced this checkpoint (checkpoint is auth-specific)
	updatedAt time.Time
}

type cursorSession struct {
	stream       *cursorproto.H2Stream
	blobStore    map[string][]byte
	mcpTools     []cursorproto.McpToolDef
	pending      []pendingMcpExec
	cancel       context.CancelFunc // cancels the session-scoped heartbeat (NOT tied to HTTP request)
	createdAt    time.Time
	authID       string                                     // auth file ID that created this session (for multi-account isolation)
	toolResultCh chan []toolResultInfo                      // receives tool results from the next HTTP request
	resumeOutCh  chan cliproxyexecutor.StreamChunk          // output channel for resumed response
	switchOutput func(ch chan cliproxyexecutor.StreamChunk) // callback to switch output channel
}

type pendingMcpExec struct {
	ExecMsgId  uint32
	ExecId     string
	ToolCallId string
	ToolName   string
	Args       string // JSON-encoded args
}

// NewCursorExecutor constructs a new executor instance.
func NewCursorExecutor(cfg *config.Config) *CursorExecutor {
	e := &CursorExecutor{
		cfg:         cfg,
		sessions:    make(map[string]*cursorSession),
		checkpoints: make(map[string]*savedCheckpoint),
		rotations:   make(map[string]string),
	}
	go e.cleanupLoop()
	return e
}

// Identifier implements ProviderExecutor.
func (e *CursorExecutor) Identifier() string { return cursorAuthType }

// CloseExecutionSession implements ExecutionSessionCloser.
func (e *CursorExecutor) CloseExecutionSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if sessionID == cliproxyauth.CloseAllExecutionSessionsID {
		for k, s := range e.sessions {
			s.cancel()
			delete(e.sessions, k)
		}
		return
	}
	if s, ok := e.sessions[sessionID]; ok {
		s.cancel()
		delete(e.sessions, sessionID)
	}
}

func (e *CursorExecutor) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		e.mu.Lock()
		for k, s := range e.sessions {
			if time.Since(s.createdAt) > cursorSessionTTL {
				s.cancel()
				delete(e.sessions, k)
			}
		}
		for k, cp := range e.checkpoints {
			if time.Since(cp.updatedAt) > cursorCheckpointTTL {
				delete(e.checkpoints, k)
			}
		}
		e.mu.Unlock()
	}
}

// findSessionByConversationLocked searches for a session matching the given
// conversationId regardless of authID. Used to find and clean up stale sessions
// from a previous auth after quota failover. Caller must hold e.mu.
func (e *CursorExecutor) findSessionByConversationLocked(convId string) string {
	suffix := ":" + convId
	for k := range e.sessions {
		if strings.HasSuffix(k, suffix) {
			return k
		}
	}
	return ""
}

// cursorStatusErr implements the StatusError and RetryAfter interfaces so the
// conductor can classify Cursor errors (e.g. 429 → quota cooldown).
type cursorStatusErr struct {
	code int
	msg  string
}

func (e cursorStatusErr) Error() string              { return e.msg }
func (e cursorStatusErr) StatusCode() int            { return e.code }
func (e cursorStatusErr) RetryAfter() *time.Duration { return nil } // no retry-after info from Cursor; conductor uses exponential backoff

// classifyCursorError maps Cursor Connect/H2 errors to HTTP status codes.
// Layer 1: precise match on ConnectError.Code (gRPC standard codes).
// Layer 2: fuzzy string match for H2 frame errors and unknown formats.
// Unclassified errors pass through unchanged.
func classifyCursorError(err error) error {
	if err == nil {
		return nil
	}

	// Layer 1: structured ConnectError from ParseConnectEndStream
	var ce *cursorproto.ConnectError
	if errors.As(err, &ce) {
		log.Infof("cursor: Connect error code=%q message=%q", ce.Code, ce.Message)
		switch ce.Code {
		case "resource_exhausted":
			return cursorStatusErr{code: 429, msg: err.Error()}
		case "unauthenticated":
			return cursorStatusErr{code: 401, msg: err.Error()}
		case "permission_denied":
			return cursorStatusErr{code: 403, msg: err.Error()}
		case "unavailable":
			return cursorStatusErr{code: 503, msg: err.Error()}
		case "internal":
			return cursorStatusErr{code: 500, msg: err.Error()}
		default:
			// Unknown Connect code — log for observation, treat as 502
			return cursorStatusErr{code: 502, msg: err.Error()}
		}
	}

	// Layer 2: fuzzy match for H2 errors and unstructured messages
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unauthenticated") || strings.Contains(msg, "unauthorized"):
		return cursorStatusErr{code: 401, msg: err.Error()}
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota") ||
		strings.Contains(msg, "too many"):
		return cursorStatusErr{code: 429, msg: err.Error()}
	case strings.Contains(msg, "rst_stream") || strings.Contains(msg, "goaway"):
		return cursorStatusErr{code: 502, msg: err.Error()}
	}

	return err
}

// PrepareRequest implements ProviderExecutor (for HttpRequest support).
func (e *CursorExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	token := cursorAccessToken(auth)
	if token == "" {
		return fmt.Errorf("cursor: access token not found")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// HttpRequest injects credentials and executes the request.
func (e *CursorExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("cursor: request is nil")
	}
	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// CountTokens estimates token count locally using tiktoken.
func (e *CursorExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	defer func() {
		if err != nil {
			log.Warnf("cursor CountTokens error: %v", err)
		} else {
			log.Debugf("cursor CountTokens: model=%s result=%s", req.Model, string(resp.Payload))
		}
	}()
	model := gjson.GetBytes(req.Payload, "model").String()
	if model == "" {
		model = req.Model
	}

	enc, err := getTokenizer(model)
	if err != nil {
		// Fallback: return zero tokens rather than error (avoids 502)
		return cliproxyexecutor.Response{Payload: buildOpenAIUsageJSON(0)}, nil
	}

	// Detect format: Claude (/v1/messages) vs OpenAI (/v1/chat/completions)
	var count int64
	if gjson.GetBytes(req.Payload, "system").Exists() || opts.SourceFormat.String() == "claude" {
		count, _ = countClaudeChatTokens(enc, req.Payload)
	} else {
		count, _ = countOpenAIChatTokens(enc, req.Payload)
	}

	return cliproxyexecutor.Response{Payload: buildOpenAIUsageJSON(count)}, nil
}

// Refresh attempts to refresh the Cursor access token.
func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	refreshToken := cursorRefreshToken(auth)
	if refreshToken == "" {
		return nil, fmt.Errorf("cursor: no refresh token available")
	}

	tokens, err := cursorauth.RefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	expiresAt := cursorauth.GetTokenExpiry(tokens.AccessToken)

	newAuth := auth.Clone()
	newAuth.Metadata["access_token"] = tokens.AccessToken
	newAuth.Metadata["refresh_token"] = tokens.RefreshToken
	newAuth.Metadata["expires_at"] = expiresAt.Format(time.RFC3339)
	return newAuth, nil
}

// Execute handles non-streaming requests.
func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	log.Debugf("cursor Execute: model=%s sourceFormat=%s payloadLen=%d", req.Model, opts.SourceFormat, len(req.Payload))
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("cursor Execute PANIC: %v", r)
			err = fmt.Errorf("cursor: internal panic: %v", r)
		}
		if err != nil {
			log.Warnf("cursor Execute error: %v", err)
		}
	}()
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("cursor: access token not found")
	}

	// Translate input to OpenAI format if needed (e.g. Claude /v1/messages format)
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	payload := req.Payload
	if from.String() != "" && from.String() != "openai" {
		payload = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(payload), false)
	}

	parsed := parseOpenAIRequest(payload)
	if req.Model != "" {
		parsed.Model = req.Model
	}
	ccSessId := extractClaudeCodeSessionId(req.Payload)
	baseConversationId := deriveConversationId(apiKeyFromContext(ctx), ccSessId, parsed.SystemPrompt)
	conversationId := e.effectiveCursorConversation(baseConversationId)
	params := buildRunRequestParams(parsed, conversationId)

	// Collect full text from the streamed Run response. A poisoned conversation
	// can return resource_exhausted before producing any token delta; in that
	// specific case retry once with a fresh wire conversation id.
	var fullText strings.Builder
	usage := &cursorTokenUsage{}
	usage.setInputEstimate(len(payload))
	for attempt := 0; ; attempt++ {
		requestBytes := cursorproto.EncodeRunRequest(params)
		stream, openErr := openCursorH2Stream(accessToken)
		if openErr != nil {
			return resp, openErr
		}
		if writeErr := stream.Write(cursorproto.FrameConnectMessage(requestBytes, 0)); writeErr != nil {
			stream.Close()
			return resp, fmt.Errorf("cursor: failed to send request: %w", writeErr)
		}

		attemptCtx, attemptCancel := context.WithCancel(ctx)
		go cursorH2Heartbeat(attemptCtx, stream)
		streamErr := processH2SessionFrames(attemptCtx, stream, params.BlobStore, nil,
			func(text string, isThinking bool) {
				fullText.WriteString(text)
			},
			nil,
			nil,
			nil,
			usage,
			nil, // non-streaming requests do not persist checkpoints
		)
		attemptCancel()
		stream.Close()

		if streamErr == nil {
			break
		}
		if attempt == 0 && !usage.sawTokenEvidence() && isCursorResourceExhausted(streamErr) {
			if rotated, ok := e.rotateCursorConversation(baseConversationId, conversationId, ""); ok {
				conversationId = rotated
				params.ConversationId = rotated
				fullText.Reset()
				continue
			}
		}
		if fullText.Len() > 0 {
			break
		}
		return resp, classifyCursorError(fmt.Errorf("cursor: stream error: %w", streamErr))
	}

	id := "chatcmpl-" + uuid.New().String()[:28]
	created := time.Now().Unix()
	openaiResp := fmt.Sprintf(`{"id":"%s","object":"chat.completion","created":%d,"model":"%s","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
		id, created, parsed.Model, jsonString(fullText.String()))

	// Translate response back to source format if needed
	result := []byte(openaiResp)
	if from.String() != "" && from.String() != "openai" {
		var param any
		result = sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), payload, result, &param)
	}
	resp.Payload = result
	return resp, nil
}

// ExecuteStream handles streaming requests.
// It supports MCP tool call sessions: when Cursor returns an MCP tool call,
// the H2 stream is kept alive. When Claude Code returns the tool result in
// the next request, the result is sent back on the same stream (session resume).
// This mirrors the activeSessions/resumeWithToolResults pattern in cursor-fetch.ts.
func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	log.Debugf("cursor ExecuteStream: model=%s sourceFormat=%s payloadLen=%d", req.Model, opts.SourceFormat, len(req.Payload))
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("cursor ExecuteStream PANIC: %v", r)
			err = fmt.Errorf("cursor: internal panic: %v", r)
		}
		if err != nil {
			log.Warnf("cursor ExecuteStream error: %v", err)
		}
	}()
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("cursor: access token not found")
	}

	// Extract session_id from metadata BEFORE translation (translation strips metadata)
	ccSessionId := extractClaudeCodeSessionId(req.Payload)
	if ccSessionId == "" && len(opts.OriginalRequest) > 0 {
		ccSessionId = extractClaudeCodeSessionId(opts.OriginalRequest)
	}

	// Translate input to OpenAI format if needed
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	payload := req.Payload
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	if from.String() != "" && from.String() != "openai" {
		log.Debugf("cursor: translating request from %s to openai", from)
		payload = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(payload), true)
		log.Debugf("cursor: translated payload len=%d", len(payload))
	}

	parsed := parseOpenAIRequest(payload)
	if req.Model != "" {
		parsed.Model = req.Model
	}
	log.Debugf("cursor: parsed request: model=%s userText=%d chars, turns=%d, tools=%d, toolResults=%d",
		parsed.Model, len(parsed.UserText), len(parsed.Turns), len(parsed.Tools), len(parsed.ToolResults))

	baseConversationId := deriveConversationId(apiKeyFromContext(ctx), ccSessionId, parsed.SystemPrompt)
	conversationId := e.effectiveCursorConversation(baseConversationId)
	authID := auth.ID // e.g. "cursor.json" or "cursor-account2.json"
	log.Debugf("cursor: conversationId=%s authID=%s", conversationId, authID)

	// Session key includes authID (H2 stream is auth-specific, not transferable).
	// Checkpoint key uses conversationId only — allows detecting auth migration.
	sessionKey := authID + ":" + conversationId
	checkpointKey := conversationId
	needsTranslate := from.String() != "" && from.String() != "openai"

	// Check if we can resume an existing session with tool results
	if len(parsed.ToolResults) > 0 {
		e.mu.Lock()
		session, hasSession := e.sessions[sessionKey]
		if hasSession {
			delete(e.sessions, sessionKey)
		}
		// If no session found for current auth, check for stale sessions from
		// a different auth on the same conversation (quota failover scenario).
		// Clean them up since the H2 stream belongs to the old account.
		if !hasSession {
			if oldKey := e.findSessionByConversationLocked(conversationId); oldKey != "" {
				oldSession := e.sessions[oldKey]
				log.Infof("cursor: cleaning up stale session from auth %s for conv=%s (auth migrated to %s)", oldSession.authID, conversationId, authID)
				oldSession.cancel()
				if oldSession.stream != nil {
					oldSession.stream.Close()
				}
				delete(e.sessions, oldKey)
			}
		}
		e.mu.Unlock()

		if hasSession && session.stream != nil && session.authID == authID {
			log.Debugf("cursor: resuming session %s with %d tool results", sessionKey, len(parsed.ToolResults))
			return e.resumeWithToolResults(ctx, session, parsed, from, to, req, originalPayload, payload, needsTranslate)
		}
		if hasSession && session.authID != authID {
			log.Warnf("cursor: session %s belongs to auth %s, but request is from %s — skipping resume", sessionKey, session.authID, authID)
		}
	}

	// Clean up any stale session for this key (or from a previous auth on same conversation)
	e.mu.Lock()
	if old, ok := e.sessions[sessionKey]; ok {
		old.cancel()
		delete(e.sessions, sessionKey)
	} else if oldKey := e.findSessionByConversationLocked(conversationId); oldKey != "" {
		old := e.sessions[oldKey]
		old.cancel()
		if old.stream != nil {
			old.stream.Close()
		}
		delete(e.sessions, oldKey)
	}
	e.mu.Unlock()

	// Look up saved checkpoint for this conversation (keyed by conversationId only).
	// Checkpoint is auth-specific: if auth changed (e.g. quota exhaustion failover),
	// the old checkpoint is useless on the new account — discard and flatten.
	e.mu.Lock()
	saved, hasCheckpoint := e.checkpoints[checkpointKey]
	e.mu.Unlock()

	params := buildRunRequestParams(parsed, conversationId)

	if hasCheckpoint && saved.data != nil && saved.authID == authID {
		// Preserve server-managed checkpoint state, but replace its history with
		// the request's freshly rebuilt root prompt blobs and turns. Cursor's
		// echoed checkpoint can contain empty historical user placeholders.
		log.Debugf("cursor: using saved checkpoint (%d bytes) for conv=%s auth=%s", len(saved.data), checkpointKey, authID)
		for k, v := range saved.blobStore {
			if _, exists := params.BlobStore[k]; !exists {
				params.BlobStore[k] = v
			}
		}
		mergedCheckpoint, mergeErr := mergeCursorCheckpointHistory(saved.data, params)
		if mergeErr != nil {
			log.Warnf("cursor: failed to merge checkpoint history for conv=%s: %v; rebuilding state", checkpointKey, mergeErr)
		} else {
			params.RawCheckpoint = mergedCheckpoint
		}
	} else if hasCheckpoint && saved.data != nil && saved.authID != authID {
		// Checkpoints are account-specific. The complete request history is
		// already rebuilt into root_prompt_messages_json and turns, so a new
		// account can start from that state without flattening it into user text.
		log.Infof("cursor: auth migrated (%s → %s) for conv=%s, discarding checkpoint and rebuilding context", saved.authID, authID, checkpointKey)
		e.mu.Lock()
		delete(e.checkpoints, checkpointKey)
		e.mu.Unlock()
	}
	requestBytes := cursorproto.EncodeRunRequest(params)
	framedRequest := cursorproto.FrameConnectMessage(requestBytes, 0)

	stream, err := openCursorH2Stream(accessToken)
	if err != nil {
		return nil, err
	}

	if err := stream.Write(framedRequest); err != nil {
		stream.Close()
		return nil, fmt.Errorf("cursor: failed to send request: %w", err)
	}

	// Use a session-scoped context that is NOT tied to the HTTP request. Run
	// heartbeats are armed per transport attempt below so conversation rotation
	// can stop the old heartbeat before opening the retry stream.
	sessionCtx, sessionCancel := context.WithCancel(context.Background())

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)
	chatId := "chatcmpl-" + uuid.New().String()[:28]
	created := time.Now().Unix()

	var streamParam any

	// Tool result channel for inline mode. processH2SessionFrames blocks on it
	// when mcpArgs is received, while continuing to handle KV/heartbeat.
	toolResultCh := make(chan []toolResultInfo, 1)

	// Switchable output: initially writes to `chunks`. After mcpArgs, the
	// onMcpExec callback closes `chunks` (ending the first HTTP response),
	// then processH2SessionFrames blocks on toolResultCh. When results arrive,
	// it switches to `resumeOutCh` (created by resumeWithToolResults).
	var outMu sync.Mutex
	currentOut := chunks

	emitToOut := func(chunk cliproxyexecutor.StreamChunk) {
		outMu.Lock()
		out := currentOut
		outMu.Unlock()
		if out != nil {
			out <- chunk
		}
	}

	// Wrap sendChunk/sendDone to use emitToOut
	sendChunkSwitchable := func(delta string, finishReason string) {
		fr := "null"
		if finishReason != "" {
			fr = finishReason
		}
		openaiJSON := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
			chatId, created, parsed.Model, delta, fr)
		sseLine := []byte("data: " + openaiJSON + "\n")

		if needsTranslate {
			translated := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, sseLine, &streamParam)
			for _, t := range translated {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: bytes.Clone(t)})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte(openaiJSON)})
		}
	}

	sendDoneSwitchable := func() {
		if needsTranslate {
			done := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, []byte("data: [DONE]\n"), &streamParam)
			for _, d := range done {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: bytes.Clone(d)})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte("[DONE]")})
		}
	}

	// Pre-response error detection for transparent failover:
	// If the stream fails before any chunk is emitted (e.g. quota exceeded),
	// ExecuteStream returns an error so the conductor retries with a different auth.
	streamErrCh := make(chan error, 1)
	firstChunkSent := make(chan struct{}, 1) // buffered: goroutine won't block signaling

	origEmitToOut := emitToOut
	emitToOut = func(chunk cliproxyexecutor.StreamChunk) {
		select {
		case firstChunkSent <- struct{}{}:
		default:
		}
		origEmitToOut(chunk)
	}

	go func() {
		var resumeOutCh chan cliproxyexecutor.StreamChunk
		_ = resumeOutCh
		thinkingActive := false
		toolCallIndex := 0
		usage := &cursorTokenUsage{}
		usage.setInputEstimate(len(payload))
		type streamedToolCall struct {
			index int
			args  string
		}
		streamedToolCalls := make(map[string]*streamedToolCall)

		processAttempt := func() error {
			attemptCtx, attemptCancel := context.WithCancel(sessionCtx)
			go cursorH2Heartbeat(attemptCtx, stream)
			defer attemptCancel()
			return processH2SessionFrames(attemptCtx, stream, params.BlobStore, params.McpTools,
				func(text string, isThinking bool) {
					if isThinking {
						if !thinkingActive {
							thinkingActive = true
							sendChunkSwitchable(`{"role":"assistant","content":"<think>"}`, "")
						}
						sendChunkSwitchable(fmt.Sprintf(`{"content":%s}`, jsonString(text)), "")
					} else {
						if thinkingActive {
							thinkingActive = false
							sendChunkSwitchable(`{"content":"</think>"}`, "")
						}
						sendChunkSwitchable(fmt.Sprintf(`{"content":%s}`, jsonString(text)), "")
					}
				},
				func(update cursorToolCallUpdate) {
					key := update.ToolCallID
					if key == "" {
						key = update.CallID
					}
					switch update.Kind {
					case cursorToolCallStarted:
						if key == "" || streamedToolCalls[key] != nil {
							return
						}
						call := &streamedToolCall{index: toolCallIndex}
						toolCallIndex++
						streamedToolCalls[key] = call
						if update.CallID != "" {
							streamedToolCalls[update.CallID] = call
						}
						if update.ToolCallID != "" {
							streamedToolCalls[update.ToolCallID] = call
						}
						toolCallJSON := fmt.Sprintf(`{"tool_calls":[{"index":%d,"id":%s,"type":"function","function":{"name":%s,"arguments":""}}]}`,
							call.index, jsonString(key), jsonString(update.ToolName))
						sendChunkSwitchable(toolCallJSON, "")
					case cursorToolCallArgumentsDelta:
						call := streamedToolCalls[key]
						if call == nil || update.ArgumentsDelta == "" {
							return
						}
						call.args += update.ArgumentsDelta
						toolCallJSON := fmt.Sprintf(`{"tool_calls":[{"index":%d,"function":{"arguments":%s}}]}`,
							call.index, jsonString(update.ArgumentsDelta))
						sendChunkSwitchable(toolCallJSON, "")
					}
				},
				func(exec pendingMcpExec) {
					if thinkingActive {
						thinkingActive = false
						sendChunkSwitchable(`{"content":"</think>"}`, "")
					}
					if call := streamedToolCalls[exec.ToolCallId]; call != nil {
						missing := exec.Args
						if strings.HasPrefix(exec.Args, call.args) {
							missing = strings.TrimPrefix(exec.Args, call.args)
						}
						if missing != "" {
							toolCallJSON := fmt.Sprintf(`{"tool_calls":[{"index":%d,"function":{"arguments":%s}}]}`,
								call.index, jsonString(missing))
							sendChunkSwitchable(toolCallJSON, "")
							call.args += missing
						}
					} else {
						toolCallJSON := fmt.Sprintf(`{"tool_calls":[{"index":%d,"id":%s,"type":"function","function":{"name":%s,"arguments":%s}}]}`,
							toolCallIndex, jsonString(exec.ToolCallId), jsonString(exec.ToolName), jsonString(exec.Args))
						toolCallIndex++
						sendChunkSwitchable(toolCallJSON, "")
					}
					sendChunkSwitchable(`{}`, `"tool_calls"`)
					sendDoneSwitchable()

					// Close current output to end the current HTTP SSE response
					outMu.Lock()
					if currentOut != nil {
						close(currentOut)
						currentOut = nil
					}
					outMu.Unlock()

					// Create new resume output channel, reuse the same toolResultCh
					resumeOut := make(chan cliproxyexecutor.StreamChunk, 64)
					log.Debugf("cursor: saving session %s for MCP tool resume (tool=%s)", sessionKey, exec.ToolName)
					e.mu.Lock()
					e.sessions[sessionKey] = &cursorSession{
						stream:       stream,
						blobStore:    params.BlobStore,
						mcpTools:     params.McpTools,
						pending:      []pendingMcpExec{exec},
						cancel:       sessionCancel,
						createdAt:    time.Now(),
						authID:       authID,
						toolResultCh: toolResultCh, // reuse same channel across rounds
						resumeOutCh:  resumeOut,
						switchOutput: func(ch chan cliproxyexecutor.StreamChunk) {
							outMu.Lock()
							currentOut = ch
							// Reset translator state so the new HTTP response gets
							// a fresh message_start, content_block_start, etc.
							streamParam = nil
							// New response needs its own message ID
							chatId = "chatcmpl-" + uuid.New().String()[:28]
							created = time.Now().Unix()
							outMu.Unlock()
						},
					}
					e.mu.Unlock()
					resumeOutCh = resumeOut

					// processH2SessionFrames will now block on toolResultCh (inline wait loop)
					// while continuing to handle KV messages
				},
				toolResultCh,
				usage,
				func(cpData []byte) {
					// Save checkpoint keyed by conversationId, tagged with authID for migration detection
					e.mu.Lock()
					e.checkpoints[checkpointKey] = &savedCheckpoint{
						data:      cpData,
						blobStore: params.BlobStore,
						authID:    authID,
						updatedAt: time.Now(),
					}
					e.mu.Unlock()
					log.Debugf("cursor: saved checkpoint (%d bytes) for conv=%s auth=%s", len(cpData), checkpointKey, authID)
				},
			)
		}

		streamErr := processAttempt()
		if streamErr != nil && !usage.sawTokenEvidence() && isCursorResourceExhausted(streamErr) {
			if rotated, ok := e.rotateCursorConversation(baseConversationId, conversationId, authID); ok {
				log.Infof("cursor: rotating resource-exhausted conversation %s to %s and retrying Run once", conversationId, rotated)
				stream.Close()
				conversationId = rotated
				sessionKey = authID + ":" + rotated
				checkpointKey = rotated
				params.ConversationId = rotated
				requestBytes = cursorproto.EncodeRunRequest(params)
				retryStream, retryErr := openCursorH2Stream(accessToken)
				if retryErr == nil {
					retryErr = retryStream.Write(cursorproto.FrameConnectMessage(requestBytes, 0))
				}
				if retryErr == nil {
					stream = retryStream
					streamErr = processAttempt()
				} else {
					if retryStream != nil {
						retryStream.Close()
					}
					streamErr = fmt.Errorf("cursor: failed to retry rotated conversation: %w", retryErr)
				}
			}
		}

		// processH2SessionFrames returned — stream is done.
		// Check if error happened before any chunks were emitted.
		if streamErr != nil {
			select {
			case <-firstChunkSent:
				// Chunks were already sent to client — can't transparently retry.
				// Next request will failover via conductor's cooldown mechanism.
				log.Warnf("cursor: stream error after data sent (auth=%s conv=%s): %v", authID, conversationId, streamErr)
			default:
				// No data sent yet — propagate error for transparent conductor retry.
				log.Warnf("cursor: stream error before data sent (auth=%s conv=%s): %v — signaling retry", authID, conversationId, streamErr)
				streamErrCh <- streamErr
				outMu.Lock()
				if currentOut != nil {
					close(currentOut)
					currentOut = nil
				}
				outMu.Unlock()
				sessionCancel()
				stream.Close()
				return
			}
		}

		if thinkingActive {
			sendChunkSwitchable(`{"content":"</think>"}`, "")
		}
		// Include token usage in the final stop chunk
		inputTok, outputTok := usage.get()
		stopDelta := fmt.Sprintf(`{},"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}`,
			inputTok, outputTok, inputTok+outputTok)
		// Build the stop chunk with usage embedded in the choices array level
		fr := `"stop"`
		openaiJSON := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":{},"finish_reason":%s}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
			chatId, created, parsed.Model, fr, inputTok, outputTok, inputTok+outputTok)
		sseLine := []byte("data: " + openaiJSON + "\n")
		if needsTranslate {
			translated := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, sseLine, &streamParam)
			for _, t := range translated {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: bytes.Clone(t)})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte(openaiJSON)})
		}
		sendDoneSwitchable()
		_ = stopDelta // unused

		// Close whatever output channel is still active
		outMu.Lock()
		if currentOut != nil {
			close(currentOut)
			currentOut = nil
		}
		outMu.Unlock()
		sessionCancel()
		stream.Close()
	}()

	// Wait for either the first chunk or a pre-response error.
	// If the stream fails before emitting any data (e.g. quota exceeded),
	// return an error so the conductor retries with a different auth.
	select {
	case streamErr := <-streamErrCh:
		return nil, classifyCursorError(fmt.Errorf("cursor: stream failed before response: %w", streamErr))
	case <-firstChunkSent:
		// Data started flowing — return stream to client
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
}

// resumeWithToolResults injects tool results into the running processH2SessionFrames
// via the toolResultCh channel. The original goroutine from ExecuteStream is still alive,
// blocking on toolResultCh. Once we send the results, it sends the MCP result to Cursor
// and continues processing the response text — all in the same goroutine that has been
// handling KV messages the whole time.
func (e *CursorExecutor) resumeWithToolResults(
	ctx context.Context,
	session *cursorSession,
	parsed *parsedOpenAIRequest,
	from, to sdktranslator.Format,
	req cliproxyexecutor.Request,
	originalPayload, payload []byte,
	needsTranslate bool,
) (*cliproxyexecutor.StreamResult, error) {
	log.Debugf("cursor: resumeWithToolResults: injecting %d tool results via channel", len(parsed.ToolResults))

	if session.toolResultCh == nil {
		return nil, fmt.Errorf("cursor: session has no toolResultCh (stale session?)")
	}
	if session.resumeOutCh == nil {
		return nil, fmt.Errorf("cursor: session has no resumeOutCh")
	}

	log.Debugf("cursor: resumeWithToolResults: switching output to resumeOutCh and injecting results")

	// Switch the output channel BEFORE injecting results, so that when
	// processH2SessionFrames unblocks and starts emitting text, it writes
	// to the resumeOutCh which the new HTTP handler is reading from.
	if session.switchOutput != nil {
		session.switchOutput(session.resumeOutCh)
	}

	// Inject tool results — this unblocks the waiting processH2SessionFrames
	session.toolResultCh <- parsed.ToolResults

	// Return the resumeOutCh for the new HTTP handler to read from
	return &cliproxyexecutor.StreamResult{Chunks: session.resumeOutCh}, nil
}

// --- H2Stream helpers ---

func openCursorH2Stream(accessToken string) (*cursorproto.H2Stream, error) {
	headers := cursorRunHeaders(accessToken)
	return cursorproto.DialH2Stream(cursorAgentHost, headers)
}

func cursorRunHeaders(accessToken string) map[string]string {
	accessToken = normalizeCursorAccessToken(accessToken)
	requestID := uuid.New().String()
	// Match senpi's fixed Connect headers. Do not advertise compression or add
	// tracing headers that the decoder/transport does not otherwise implement.
	return map[string]string{
		":path":                    cursorRunPath,
		"content-type":             "application/connect+proto",
		"connect-protocol-version": "1",
		"te":                       "trailers",
		"authorization":            "Bearer " + accessToken,
		"x-ghost-mode":             "true",
		"x-cursor-client-version":  cursorClientVersion,
		"x-cursor-client-type":     "cli",
		"x-request-id":             requestID,
	}
}

func normalizeCursorAccessToken(accessToken string) string {
	if i := strings.Index(accessToken, "::"); i >= 0 {
		return accessToken[i+2:]
	}
	return accessToken
}

func cursorH2Heartbeat(ctx context.Context, stream *cursorproto.H2Stream) {
	ticker := time.NewTicker(cursorHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hb := cursorproto.EncodeHeartbeat()
			frame := cursorproto.FrameConnectMessage(hb, 0)
			if err := stream.Write(frame); err != nil {
				return
			}
		}
	}
}

// --- Response processing ---

// cursorTokenUsage tracks token counts from Cursor's TokenDeltaUpdate and the
// production TurnEndedUpdate billed split (schema 2026.08.11). The billed split
// is authoritative for context accounting; the tokenDelta-accumulated output is
// kept only when the server omits the billed output field. Reasoning tokens are
// never folded into output (no Usage field represents them).
type cursorTokenUsage struct {
	mu             sync.Mutex
	outputTokens   int64
	inputTokens    int64 // billed input (cache-INCLUSIVE remainder backed out)
	cacheRead      int64
	cacheWrite     int64
	inputTokensEst int64 // fallback only when Cursor emits no token_delta/turnEnded
	sawDelta       bool
	sawTurnEnded   bool
}

func (u *cursorTokenUsage) addOutput(delta int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.sawDelta = true
	u.outputTokens += delta
}

// applyBilledTurnEndedUsage copies Cursor's authoritative billed split onto the
// usage. input_tokens is cache-INCLUSIVE on api2.cursor.sh, so the uncached
// remainder is backed out for CPA's exclusive usage.input.
func (u *cursorTokenUsage) applyBilledTurnEndedUsage(input, output, cacheRead, cacheWrite *int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if input == nil && output == nil && cacheRead == nil && cacheWrite == nil {
		return
	}
	u.sawTurnEnded = true
	if cacheRead != nil {
		u.cacheRead = *cacheRead
	}
	if cacheWrite != nil {
		u.cacheWrite = *cacheWrite
	}
	if output != nil {
		u.outputTokens = *output
	}
	if input != nil {
		// input_tokens is cache-INCLUSIVE on api2.cursor.sh; back out cache for CPA exclusive input
		u.inputTokens = *input - u.cacheRead - u.cacheWrite
		if u.inputTokens < 0 {
			u.inputTokens = 0
		}
	}
}

func (u *cursorTokenUsage) setInputEstimate(payloadBytes int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	// Rough estimate: ~4 bytes per token for mixed content
	u.inputTokensEst = int64(payloadBytes / 4)
	if u.inputTokensEst < 1 {
		u.inputTokensEst = 1
	}
}

func (u *cursorTokenUsage) get() (input, output int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.sawTurnEnded || u.sawDelta {
		return u.inputTokens, u.outputTokens
	}
	return u.inputTokensEst, 0
}

func (u *cursorTokenUsage) sawTokenDelta() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.sawDelta
}

// sawTokenEvidence reports whether the stream produced or billed any tokens.
// A resource_exhausted end-stream WITH evidence is a mid-flight context
// overflow; WITHOUT it, the conversation is poisoned/rate-limited and must stay
// on the rotation path.
func (u *cursorTokenUsage) sawTokenEvidence() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.sawDelta || u.sawTurnEnded || u.outputTokens > 0 || u.inputTokens > 0 || u.cacheRead > 0 || u.cacheWrite > 0
}

// applyCheckpointTokenDetails feeds a checkpoint's tokenDetails.usedTokens (the
// server's live conversation size, sent mid-turn) into the in-flight usage. It
// never overrides the billed turnEnded split once that arrived.
func (u *cursorTokenUsage) applyCheckpointTokenDetails(usedTokens int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.sawTurnEnded {
		return
	}
	if usedTokens <= 0 {
		return
	}
	input := usedTokens - u.outputTokens - u.cacheRead - u.cacheWrite
	if input < 0 {
		input = 0
	}
	u.inputTokens = input
}

// --- OpenAI request parsing ---

type parsedOpenAIRequest struct {
	Model           string
	ReasoningEffort string
	Messages        []gjson.Result
	Tools           []gjson.Result
	Stream          bool
	SystemPrompt    string
	UserText        string
	Images          []cursorproto.ImageData
	Turns           []cursorproto.TurnData
	ToolResults     []toolResultInfo
	ActiveUserIndex int
}

type toolResultInfo struct {
	ToolCallId string
	Content    string
}

func parseOpenAIRequest(payload []byte) *parsedOpenAIRequest {
	p := &parsedOpenAIRequest{
		Model:           gjson.GetBytes(payload, "model").String(),
		ReasoningEffort: extractCursorReasoningEffort(payload),
		Stream:          gjson.GetBytes(payload, "stream").Bool(),
		ActiveUserIndex: -1,
	}

	messages := gjson.GetBytes(payload, "messages").Array()
	p.Messages = messages
	p.Tools = gjson.GetBytes(payload, "tools").Array()

	// Cursor's normal system prompt lives in the root prompt blob head. There
	// is no separate CPA custom-system-prompt override concept.
	var systemParts []string
	for _, msg := range messages {
		switch msg.Get("role").String() {
		case "system", "developer":
			if text := strings.TrimSpace(extractTextContent(msg.Get("content"))); text != "" {
				systemParts = append(systemParts, text)
			}
		}
	}
	if len(systemParts) == 0 {
		p.SystemPrompt = "You are a helpful assistant."
	} else {
		p.SystemPrompt = strings.Join(systemParts, "\n")
	}

	// senpi only treats the final message as active when it is a user message.
	// A trailing assistant/tool message resumes the conversation instead.
	if len(messages) > 0 && messages[len(messages)-1].Get("role").String() == "user" {
		p.ActiveUserIndex = len(messages) - 1
		content := messages[p.ActiveUserIndex].Get("content")
		p.UserText = strings.TrimSpace(extractTextContent(content))
		p.Images = extractImages(content)
	}

	// Only a contiguous trailing tool-result suffix belongs to the currently
	// pending MCP round. Older tool messages remain history and must not trigger
	// a stale H2 session resume.
	for i := len(messages) - 1; i >= 0 && messages[i].Get("role").String() == "tool"; i-- {
		p.ToolResults = append(p.ToolResults, toolResultInfo{
			ToolCallId: messages[i].Get("tool_call_id").String(),
			Content:    extractTextContent(messages[i].Get("content")),
		})
	}
	for left, right := 0, len(p.ToolResults)-1; left < right; left, right = left+1, right-1 {
		p.ToolResults[left], p.ToolResults[right] = p.ToolResults[right], p.ToolResults[left]
	}

	// Rebuild prior user/assistant turns for ConversationStateStructure.turns.
	// Root prompt blobs below carry the richer tool/image history used for the
	// actual model prompt.
	historyEnd := len(messages)
	if p.ActiveUserIndex >= 0 {
		historyEnd = p.ActiveUserIndex
	}
	var pending *cursorproto.TurnData
	for i := 0; i < historyEnd; i++ {
		msg := messages[i]
		switch msg.Get("role").String() {
		case "user":
			if pending != nil {
				p.Turns = append(p.Turns, *pending)
			}
			text := strings.TrimSpace(extractTextContent(msg.Get("content")))
			if text == "" {
				pending = nil
			} else {
				pending = &cursorproto.TurnData{UserText: text}
			}
		case "assistant":
			assistantText := extractTextContent(msg.Get("content"))
			if pending != nil {
				pending.AssistantText = assistantText
				p.Turns = append(p.Turns, *pending)
				pending = nil
			} else if assistantText != "" && len(p.Turns) > 0 {
				last := &p.Turns[len(p.Turns)-1]
				if last.AssistantText == "" {
					last.AssistantText = assistantText
				} else {
					last.AssistantText += "\n" + assistantText
				}
			}
		}
	}
	if pending != nil {
		p.Turns = append(p.Turns, *pending)
	}

	return p
}

// flattenConversationIntoUserText remains available as a last-resort fallback
// for callers that explicitly need a plain-text replay. Normal Run requests use
// structured root prompt blobs and turns instead.
func flattenConversationIntoUserText(parsed *parsedOpenAIRequest) {
	var buf strings.Builder
	for _, turn := range parsed.Turns {
		if turn.UserText != "" {
			buf.WriteString("USER: ")
			buf.WriteString(turn.UserText)
			buf.WriteString("\n\n")
		}
		if turn.AssistantText != "" {
			buf.WriteString("ASSISTANT: ")
			buf.WriteString(turn.AssistantText)
			buf.WriteString("\n\n")
		}
	}
	for _, tr := range parsed.ToolResults {
		buf.WriteString("TOOL_RESULT (call_id: ")
		buf.WriteString(tr.ToolCallId)
		buf.WriteString("): ")
		content := tr.Content
		if len(content) > 8000 {
			content = content[:8000] + "\n... [truncated]"
		}
		buf.WriteString(content)
		buf.WriteString("\n\n")
	}
	if buf.Len() > 0 {
		buf.WriteString("The above is the previous conversation context including tool call results.\n")
		buf.WriteString("Continue your response based on this context.\n\n")
	}
	if parsed.UserText != "" {
		parsed.UserText = buf.String() + "Current request: " + parsed.UserText
	} else {
		parsed.UserText = buf.String() + "Continue from the conversation above."
	}
	parsed.Images = nil
	parsed.Turns = nil
	parsed.ToolResults = nil
}

func extractTextContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0, len(content.Array()))
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "text", "input_text", "output_text":
			if text := part.Get("text").String(); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func extractImages(content gjson.Result) []cursorproto.ImageData {
	if !content.IsArray() {
		return nil
	}
	var images []cursorproto.ImageData
	for _, part := range content.Array() {
		image := cursorImageFromContentPart(part)
		if image == nil || len(image.Data) == 0 {
			continue
		}
		images = append(images, *image)
	}
	return images
}

func cursorImageFromContentPart(part gjson.Result) *cursorproto.ImageData {
	var dataURL, mimeType, encoded string
	switch part.Get("type").String() {
	case "image_url":
		dataURL = part.Get("image_url.url").String()
		if dataURL == "" && part.Get("image_url").Type == gjson.String {
			dataURL = part.Get("image_url").String()
		}
	case "input_image":
		dataURL = part.Get("image_url").String()
		if dataURL == "" {
			dataURL = part.Get("image_url.url").String()
		}
	case "image":
		dataURL = part.Get("image_url").String()
		if dataURL == "" {
			dataURL = part.Get("url").String()
		}
		mimeType = part.Get("mime_type").String()
		if mimeType == "" {
			mimeType = part.Get("mimeType").String()
		}
		encoded = part.Get("data").String()
		if encoded == "" {
			encoded = part.Get("source.data").String()
		}
		if mimeType == "" {
			mimeType = part.Get("source.media_type").String()
		}
	default:
		return nil
	}

	if strings.HasPrefix(dataURL, "data:") {
		return parseDataURL(dataURL)
	}
	if encoded == "" {
		return nil
	}
	data, ok := decodeCursorBase64(encoded)
	if !ok {
		return nil
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return &cursorproto.ImageData{MimeType: mimeType, Data: data}
}

func parseDataURL(url string) *cursorproto.ImageData {
	if !strings.HasPrefix(url, "data:") {
		return nil
	}
	comma := strings.IndexByte(url, ',')
	if comma < 0 {
		return nil
	}
	metadata := strings.Split(url[5:comma], ";")
	if len(metadata) == 0 {
		return nil
	}
	base64Encoded := false
	for _, item := range metadata[1:] {
		if strings.EqualFold(item, "base64") {
			base64Encoded = true
			break
		}
	}
	if !base64Encoded {
		return nil
	}
	data, ok := decodeCursorBase64(url[comma+1:])
	if !ok {
		return nil
	}
	mimeType := metadata[0]
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return &cursorproto.ImageData{MimeType: mimeType, Data: data}
}

func decodeCursorBase64(encoded string) ([]byte, bool) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err == nil {
		return data, true
	}
	data, err = base64.RawStdEncoding.DecodeString(encoded)
	return data, err == nil
}

func buildRunRequestParams(parsed *parsedOpenAIRequest, conversationId string) *cursorproto.RunRequestParams {
	params := &cursorproto.RunRequestParams{
		ModelId:            parsed.Model,
		ReasoningEffort:    parsed.ReasoningEffort,
		DisplayModelId:     parsed.Model,
		DisplayName:        parsed.Model,
		SystemPrompt:       parsed.SystemPrompt,
		UserText:           parsed.UserText,
		MessageId:          uuid.New().String(),
		ConversationId:     conversationId,
		Resume:             parsed.ActiveUserIndex < 0 || (parsed.UserText == "" && len(parsed.Images) == 0),
		Images:             parsed.Images,
		Turns:              parsed.Turns,
		RootPromptMessages: buildCursorRootPromptMessages(parsed),
		BlobStore:          make(map[string][]byte),
	}

	// Preserve CPA's MCP relay: definitions are held for the server-driven
	// request-context exec response, while EncodeRunRequest omits top-level
	// AgentRunRequest.mcp_tools like senpi.
	for _, tool := range parsed.Tools {
		fn := tool.Get("function")
		params.McpTools = append(params.McpTools, cursorproto.McpToolDef{
			Name:        fn.Get("name").String(),
			Description: fn.Get("description").String(),
			// Cursor's gateway rejects tools whose input schema carries
			// oneOf/anyOf/allOf (resource_exhausted); strip them before the
			// schema reaches the exec context.
			InputSchema: json.RawMessage(cursorproto.SanitizeCursorToolSchema([]byte(fn.Get("parameters").Raw))),
		})
	}

	return params
}

func buildCursorRootPromptMessages(parsed *parsedOpenAIRequest) [][]byte {
	historyEnd := len(parsed.Messages)
	if parsed.ActiveUserIndex >= 0 {
		historyEnd = parsed.ActiveUserIndex
	}

	toolNames := make(map[string]string)
	var roots [][]byte
	appendJSON := func(value any) {
		data, err := json.Marshal(value)
		if err == nil {
			roots = append(roots, data)
		}
	}

	for i := 0; i < historyEnd; i++ {
		message := parsed.Messages[i]
		switch message.Get("role").String() {
		case "system", "developer":
			// The combined system prompt is already the first root blob.
			continue
		case "user":
			content := buildCursorRootUserContent(message.Get("content"))
			if len(content) > 0 {
				appendJSON(map[string]any{"role": "user", "content": content})
			}
		case "assistant":
			content := buildCursorRootAssistantContent(message, toolNames)
			if len(content) > 0 {
				appendJSON(map[string]any{"role": "assistant", "content": content})
			}
		case "tool":
			toolCallID := message.Get("tool_call_id").String()
			toolName := message.Get("name").String()
			if toolName == "" {
				toolName = toolNames[toolCallID]
			}
			item := map[string]any{
				"type":       "tool-result",
				"toolName":   toolName,
				"toolCallId": toolCallID,
				"result":     extractTextContent(message.Get("content")),
			}
			appendJSON(map[string]any{
				"role":    "tool",
				"id":      toolCallID,
				"content": []any{item},
			})
		}
	}
	return roots
}

func buildCursorRootUserContent(content gjson.Result) []any {
	if content.Type == gjson.String {
		if text := strings.TrimSpace(content.String()); text != "" {
			return []any{map[string]any{"type": "text", "text": text}}
		}
		return nil
	}
	if !content.IsArray() {
		return nil
	}
	var result []any
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "text", "input_text", "output_text":
			if text := strings.TrimSpace(part.Get("text").String()); text != "" {
				result = append(result, map[string]any{"type": "text", "text": text})
			}
		case "image_url", "input_image", "image":
			if image, mimeType := cursorRootImageReference(part); image != "" {
				result = append(result, map[string]any{"type": "image", "image": image, "mediaType": mimeType})
			}
		}
	}
	return result
}

func cursorRootImageReference(part gjson.Result) (image, mimeType string) {
	mimeType = part.Get("mime_type").String()
	if mimeType == "" {
		mimeType = part.Get("mimeType").String()
	}
	if mimeType == "" {
		mimeType = part.Get("source.media_type").String()
	}
	switch part.Get("type").String() {
	case "image_url":
		image = part.Get("image_url.url").String()
		if image == "" && part.Get("image_url").Type == gjson.String {
			image = part.Get("image_url").String()
		}
	case "input_image":
		image = part.Get("image_url").String()
		if image == "" {
			image = part.Get("image_url.url").String()
		}
	case "image":
		image = part.Get("image_url").String()
		if image == "" {
			image = part.Get("url").String()
		}
		encoded := part.Get("data").String()
		if encoded == "" {
			encoded = part.Get("source.data").String()
		}
		if image == "" && encoded != "" {
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			image = "data:" + mimeType + ";base64," + encoded
		}
	}
	if strings.HasPrefix(image, "data:") {
		if parsed := parseDataURL(image); parsed != nil {
			mimeType = parsed.MimeType
		}
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return image, mimeType
}

func buildCursorRootAssistantContent(message gjson.Result, toolNames map[string]string) []any {
	var content []any
	messageContent := message.Get("content")
	if messageContent.Type == gjson.String {
		if text := messageContent.String(); text != "" {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
	} else if messageContent.IsArray() {
		for _, part := range messageContent.Array() {
			switch part.Get("type").String() {
			case "text", "output_text":
				if text := part.Get("text").String(); text != "" {
					content = append(content, map[string]any{"type": "text", "text": text})
				}
			}
		}
	}

	for _, call := range message.Get("tool_calls").Array() {
		toolCallID := call.Get("id").String()
		toolName := call.Get("function.name").String()
		if toolCallID != "" {
			toolNames[toolCallID] = toolName
		}
		args := make(map[string]any)
		if rawArgs := strings.TrimSpace(call.Get("function.arguments").String()); rawArgs != "" {
			_ = json.Unmarshal([]byte(rawArgs), &args)
		}
		content = append(content, map[string]any{
			"type":       "tool-call",
			"toolCallId": toolCallID,
			"toolName":   toolName,
			"args":       args,
		})
	}
	return content
}

func mergeCursorCheckpointHistory(checkpoint []byte, params *cursorproto.RunRequestParams) ([]byte, error) {
	freshParams := *params
	freshParams.RawCheckpoint = nil
	freshRequest := cursorproto.EncodeRunRequest(&freshParams)
	freshState, err := cursorWireBytesField(freshRequest, cursorproto.ACM_RunRequest)
	if err != nil {
		return nil, err
	}
	freshState, err = cursorWireBytesField(freshState, cursorproto.ARR_ConversationState)
	if err != nil {
		return nil, err
	}

	preserved, err := filterCursorWireFields(checkpoint, func(fieldNumber int) bool {
		return fieldNumber != cursorproto.CSS_RootPromptMessagesJson && fieldNumber != cursorproto.CSS_Turns
	})
	if err != nil {
		return nil, err
	}
	history, err := filterCursorWireFields(freshState, func(fieldNumber int) bool {
		return fieldNumber == cursorproto.CSS_RootPromptMessagesJson || fieldNumber == cursorproto.CSS_Turns
	})
	if err != nil {
		return nil, err
	}
	return append(preserved, history...), nil
}

func cursorWireBytesField(data []byte, target int) ([]byte, error) {
	for len(data) > 0 {
		num, typ, tagLen := consumeTag(data)
		if tagLen < 0 {
			return nil, fmt.Errorf("invalid protobuf tag")
		}
		data = data[tagLen:]
		if typ == 2 {
			value, valueLen := consumeBytes(data)
			if valueLen < 0 {
				return nil, fmt.Errorf("invalid protobuf bytes field %d", num)
			}
			if num == target {
				return append([]byte(nil), value...), nil
			}
			data = data[valueLen:]
			continue
		}
		valueLen := consumeFieldValue(num, typ, data)
		if valueLen < 0 {
			return nil, fmt.Errorf("invalid protobuf field %d", num)
		}
		data = data[valueLen:]
	}
	return nil, fmt.Errorf("protobuf field %d not found", target)
}

func filterCursorWireFields(data []byte, keep func(fieldNumber int) bool) ([]byte, error) {
	var filtered []byte
	for len(data) > 0 {
		fieldStart := data
		num, typ, tagLen := consumeTag(data)
		if tagLen < 0 {
			return nil, fmt.Errorf("invalid protobuf tag")
		}
		valueLen := consumeFieldValue(num, typ, data[tagLen:])
		if valueLen < 0 {
			return nil, fmt.Errorf("invalid protobuf field %d", num)
		}
		fieldLen := tagLen + valueLen
		if keep(num) {
			filtered = append(filtered, fieldStart[:fieldLen]...)
		}
		data = fieldStart[fieldLen:]
	}
	return filtered, nil
}

// --- Helpers ---

func cursorAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["access_token"].(string); ok {
		return v
	}
	return ""
}

func cursorRefreshToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["refresh_token"].(string); ok {
		return v
	}
	return ""
}

func applyCursorHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Te", "trailers")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Ghost-Mode", "true")
	req.Header.Set("X-Cursor-Client-Version", cursorClientVersion)
	req.Header.Set("X-Cursor-Client-Type", "cli")
	req.Header.Set("X-Request-Id", uuid.New().String())
}

func newH2Client() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			TLSClientConfig: &tls.Config{},
		},
	}
}

// extractCCH extracts the cch value from the system prompt's billing header.
func extractCCH(systemPrompt string) string {
	idx := strings.Index(systemPrompt, "cch=")
	if idx < 0 {
		return ""
	}
	rest := systemPrompt[idx+4:]
	end := strings.IndexAny(rest, "; \n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// extractClaudeCodeSessionId extracts session_id from Claude Code's metadata.user_id JSON.
// Format: {"metadata":{"user_id":"{\"session_id\":\"xxx\",\"device_id\":\"yyy\"}"}}
func extractClaudeCodeSessionId(payload []byte) string {
	userIdStr := gjson.GetBytes(payload, "metadata.user_id").String()
	if userIdStr == "" {
		return ""
	}
	// user_id is a JSON string that needs to be parsed again
	sid := gjson.Get(userIdStr, "session_id").String()
	return sid
}

// deriveConversationId generates a deterministic conversation_id.
// Priority: session_id (stable across resume) > system prompt hash (fallback).
func deriveConversationId(apiKey, sessionId, _ string) string {
	if sessionId == "" {
		return uuid.New().String()
	}

	// Keep a stable UUID-shaped identity for Claude Code sessions while
	// avoiding collisions between unrelated OpenAI clients that share a proxy
	// API key and default system prompt.
	input := "cursor-conv:" + apiKey + ":" + sessionId
	h := sha256.Sum256([]byte(input))
	s := hex.EncodeToString(h[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:32])
}

func deriveSessionKey(clientKey string, model string, messages []gjson.Result) string {
	var firstUserContent string
	var systemContent string
	for _, msg := range messages {
		role := msg.Get("role").String()
		if role == "user" && firstUserContent == "" {
			firstUserContent = extractTextContent(msg.Get("content"))
		} else if role == "system" && systemContent == "" {
			// System prompt differs per Claude Code session (contains cwd, session_id, etc.)
			content := extractTextContent(msg.Get("content"))
			if len(content) > 200 {
				systemContent = content[:200]
			} else {
				systemContent = content
			}
		}
	}
	// Include client API key + system prompt hash to prevent session collisions:
	// - Different users have different API keys
	// - Different Claude Code sessions have different system prompts (cwd, tools, etc.)
	input := clientKey + ":" + model + ":" + systemContent + ":" + firstUserContent
	if len(input) > 500 {
		input = input[:500]
	}
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:16]
}

func sseChunk(id string, created int64, model string, delta string, finishReason string) cliproxyexecutor.StreamChunk {
	fr := "null"
	if finishReason != "" {
		fr = finishReason
	}
	// Note: the framework's WriteChunk adds "data: " prefix and "\n\n" suffix,
	// so we only output the raw JSON here.
	data := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
		id, created, model, delta, fr)
	return cliproxyexecutor.StreamChunk{
		Payload: []byte(data),
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func decodeMcpArgsToJSON(args map[string][]byte) string {
	if len(args) == 0 {
		return "{}"
	}
	result := make(map[string]interface{})
	for k, v := range args {
		// Try protobuf Value decoding first (matches TS: toJson(ValueSchema, fromBinary(ValueSchema, value)))
		if decoded, err := cursorproto.ProtobufValueBytesToJSON(v); err == nil {
			result[k] = decoded
		} else {
			// Fallback: try raw JSON
			var jsonVal interface{}
			if err := json.Unmarshal(v, &jsonVal); err == nil {
				result[k] = jsonVal
			} else {
				result[k] = string(v)
			}
		}
	}
	b, _ := json.Marshal(result)
	return string(b)
}

// --- Model Discovery ---

// FetchCursorModels retrieves available models from Cursor's API.
func FetchCursorModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return FilterCursorModels(GetCursorFallbackModels())
	}
	accessToken = normalizeCursorAccessToken(accessToken)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// GetUsableModels is a unary RPC call (not streaming)
	// Send an empty protobuf request
	emptyReq := make([]byte, 0)

	h2Req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cursorAgentURL+cursorModelsPath, bytes.NewReader(emptyReq))
	if err != nil {
		log.Debugf("cursor: failed to create models request: %v", err)
		return FilterCursorModels(GetCursorFallbackModels())
	}

	h2Req.Header.Set("Content-Type", "application/proto")
	h2Req.Header.Set("Te", "trailers")
	h2Req.Header.Set("Authorization", "Bearer "+accessToken)
	h2Req.Header.Set("X-Ghost-Mode", "true")
	h2Req.Header.Set("X-Cursor-Client-Version", cursorClientVersion)
	h2Req.Header.Set("X-Cursor-Client-Type", "cli")

	client := newH2Client()
	resp, err := client.Do(h2Req)
	if err != nil {
		log.Debugf("cursor: models request failed: %v", err)
		return FilterCursorModels(GetCursorFallbackModels())
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debugf("cursor: models request returned status %d", resp.StatusCode)
		return FilterCursorModels(GetCursorFallbackModels())
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return FilterCursorModels(GetCursorFallbackModels())
	}

	models := parseModelsResponse(body)
	if len(models) == 0 {
		return FilterCursorModels(GetCursorFallbackModels())
	}
	return FilterCursorModels(models)
}

func parseModelsResponse(data []byte) []*registry.ModelInfo {
	// Try stripping Connect framing first
	if len(data) >= cursorproto.ConnectFrameHeaderSize {
		_, payload, _, ok := cursorproto.ParseConnectFrame(data)
		if ok {
			data = payload
		}
	}

	// The response is a GetUsableModelsResponse protobuf.
	// We need to decode it manually - it contains a repeated "models" field.
	// Based on the TS code, the response has a `models` field (repeated) containing
	// model objects with modelId, displayName, thinkingDetails, etc.

	// For now, we'll try a simple decode approach
	var models []*registry.ModelInfo
	// Field 1 is likely "models" (repeated submessage)
	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]

		if typ == 2 { // BytesType (submessage)
			val, n := consumeBytes(data)
			if n < 0 {
				break
			}
			data = data[n:]

			if num == 1 { // models field
				if m := parseModelEntry(val); m != nil {
					models = append(models, m)
				}
			}
		} else {
			n := consumeFieldValue(num, typ, data)
			if n < 0 {
				break
			}
			data = data[n:]
		}
	}

	return models
}

func parseModelEntry(data []byte) *registry.ModelInfo {
	var modelId, displayName string
	var hasThinking bool

	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]

		switch typ {
		case 2: // BytesType
			val, n := consumeBytes(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
			switch num {
			case 1: // modelId
				modelId = string(val)
			case 2: // thinkingDetails
				hasThinking = true
			case 3: // displayModelId (use as fallback)
				if displayName == "" {
					displayName = string(val)
				}
			case 4: // displayName
				displayName = string(val)
			case 5: // displayNameShort
				if displayName == "" {
					displayName = string(val)
				}
			}
		case 0: // VarintType
			_, n := consumeVarint(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		default:
			n := consumeFieldValue(num, typ, data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		}
	}

	if modelId == "" {
		return nil
	}
	if displayName == "" {
		displayName = modelId
	}

	info := &registry.ModelInfo{
		ID:                  modelId,
		Object:              "model",
		Created:             time.Now().Unix(),
		OwnedBy:             "cursor",
		Type:                cursorAuthType,
		DisplayName:         displayName,
		ContextLength:       200000,
		MaxCompletionTokens: 64000,
	}
	if hasThinking {
		info.Thinking = &registry.ThinkingSupport{
			Max:            50000,
			DynamicAllowed: true,
		}
	}
	return info
}

func extractCursorReasoningEffort(payload []byte) string {
	if v := strings.TrimSpace(gjson.GetBytes(payload, "reasoning_effort").String()); v != "" {
		return v
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "reasoning.effort").String())
}

// GetCursorFallbackModels returns hardcoded fallback models.
func GetCursorFallbackModels() []*registry.ModelInfo {
	return []*registry.ModelInfo{
		{ID: "composer-2.5", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Composer 2.5", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "composer-2", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Composer 2", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "grok-4.5-high", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Grok 4.5", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "grok-4.6-xhigh", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Grok 4.6", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "grok-4.5-fast", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Grok 4.5 Fast", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "grok-4.6-fast", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Grok 4.6 Fast", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-4-sonnet", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Claude 4 Sonnet", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-3.5-sonnet", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Claude 3.5 Sonnet", ContextLength: 200000, MaxCompletionTokens: 8192},
		{ID: "gpt-4o", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "GPT-4o", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "cursor-small", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Cursor Small", ContextLength: 200000, MaxCompletionTokens: 64000},
		{ID: "gemini-2.5-pro", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Gemini 2.5 Pro", ContextLength: 1000000, MaxCompletionTokens: 65536, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
	}
}

// Low-level protowire helpers (avoid importing protowire in executor)
func consumeTag(b []byte) (num int, typ int, n int) {
	v, n := consumeVarint(b)
	if n < 0 {
		return 0, 0, -1
	}
	return int(v >> 3), int(v & 7), n
}

func consumeVarint(b []byte) (uint64, int) {
	var val uint64
	for i := 0; i < len(b) && i < 10; i++ {
		val |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return val, i + 1
		}
	}
	return 0, -1
}

func consumeBytes(b []byte) ([]byte, int) {
	length, n := consumeVarint(b)
	if n < 0 || int(length) > len(b)-n {
		return nil, -1
	}
	return b[n : n+int(length)], n + int(length)
}

func consumeFieldValue(num, typ int, b []byte) int {
	switch typ {
	case 0: // Varint
		_, n := consumeVarint(b)
		return n
	case 1: // 64-bit
		if len(b) < 8 {
			return -1
		}
		return 8
	case 2: // Length-delimited
		_, n := consumeBytes(b)
		return n
	case 5: // 32-bit
		if len(b) < 4 {
			return -1
		}
		return 4
	default:
		return -1
	}
}

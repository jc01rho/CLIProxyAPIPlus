// Package executor implements the Devin (Cognition) provider executor.
//
// Devin CLI speaks Connect RPC (application/proto) against a per-account API
// server URL: POST {api_server}/exa.api_server_pb.ApiServerService/GetChatMessage
// with content-type application/connect+proto. Auth is
// "authorization: Basic <windsurf_api_key>-<windsurf_api_key>" (the same token
// string repeated twice, no userinfo split).
//
// Request (verified against Devin CLI 3000.10.21 traffic):
//
//	f1 client context msg {f1="devin-cli", f2=version, f3=session token,
//	                       f4=locale, f5=os, f7=version, f12/f28="chisel",
//	                       f31=732-hex client fingerprint}
//	f2 system prompt (string)
//	f3 user message block {f1=session UUID, f2=1 (user role), f3=text}
//	f7 varint 5, f8 model config msg, f15/f16 session UUIDs,
//	f20 varint 1, f21 model UID string (e.g. "swe-1-6-fast")
//
// Response: Connect enveloped stream (0x00 + u32be length data frames,
// 0x02 + trailer JSON). Each data frame carries f9 = generated text delta,
// f2 = timestamp, f7 = metadata (assigned model UID), f17 = turn UUID.
//
// The endpoint requires a live session the server created; requests replayed
// from dead sessions fail with failed_precondition, and model UIDs the
// account cannot access fail with permission_denied.
package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// Devin API surface, verified against Devin CLI 3000.10.21.
const (
	devinConnectProtoContentType = "application/connect+proto"
	devinConnectProtocolVersion  = "1"

	devinGetChatMessagePath = "/exa.api_server_pb.ApiServerService/GetChatMessage"
	devinGetUserStatusPath  = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"

	devinDefaultAPIServerURL = "https://server.codeium.com"
	devinClientName          = "devin-cli"
	devinClientVersion       = "3000.10.21"
	devinClientKind          = "chisel"

	// devinAnswerField is the protobuf field carrying the user-visible answer
	// delta; devinReasoningField carries the private chain-of-thought.
	devinAnswerField    = 3
	devinReasoningField = 9

	devinMaxNonStreamBytes = 64 << 20
	devinDefaultTimeout    = 120 * time.Second
)

// DevinExecutor is a stateless executor for the Devin (Cognition) provider.
// It builds Connect GetChatMessage requests from OpenAI-style payloads,
// streams back Connect enveloped data frames, and maps generated text deltas
// (f9) to OpenAI Chat Completions SSE chunks.
type DevinExecutor struct {
	cfg    *config.Config
	client *http.Client
}

// NewDevinExecutor creates a Devin executor.
func NewDevinExecutor(cfg *config.Config) *DevinExecutor {
	return &DevinExecutor{
		cfg:    cfg,
		client: &http.Client{Timeout: devinDefaultTimeout},
	}
}

// Identifier returns the executor identifier.
func (e *DevinExecutor) Identifier() string { return "devin" }

// CloseExecutionSession is a no-op: Devin sessions live server-side per
// request and need no client-side teardown.
func (e *DevinExecutor) CloseExecutionSession(sessionID string) {}

// CountTokens is not supported by the Devin Connect API.
func (e *DevinExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("devin: token counting is not supported")
}

// HttpRequest is not supported: Devin speaks Connect RPC with a fixed
// protobuf schema, not plain HTTP pass-through.
func (e *DevinExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("devin: direct HTTP pass-through is not supported")
}

// Refresh returns the auth unchanged: Devin session tokens are long-lived
// and rotation happens through a fresh login.
func (e *DevinExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// devinAPIKey extracts the Devin API key or session token from the auth record.
// It checks Attributes (from config or file synthesis) and Metadata (from JSON auth files).
func devinAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
			return key
		}
		if key := strings.TrimSpace(auth.Attributes["session_token"]); key != "" {
			return key
		}
		if key := strings.TrimSpace(auth.Attributes["windsurf_api_key"]); key != "" {
			return key
		}
	}
	if auth.Metadata != nil {
		for _, field := range []string{"api_key", "session_token", "windsurf_api_key", "token", "access_token"} {
			if v, ok := auth.Metadata[field].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// devinServerURL resolves the Connect API server URL: per-credential
// base_url override wins, then the executor default.
func devinServerURL(auth *cliproxyauth.Auth) string {
	raw := ""
	if auth != nil {
		if auth.Attributes != nil {
			raw = strings.TrimSpace(auth.Attributes["base_url"])
		}
		if raw == "" && auth.Metadata != nil {
			for _, field := range []string{"api_server_url", "base_url"} {
				if v, ok := auth.Metadata[field].(string); ok && strings.TrimSpace(v) != "" {
					raw = strings.TrimSpace(v)
					break
				}
			}
		}
	}
	if raw == "" {
		return devinDefaultAPIServerURL
	}
	clean := strings.TrimRight(raw, "/")
	if !strings.HasPrefix(clean, "http://") && !strings.HasPrefix(clean, "https://") {
		clean = "https://" + clean
	}
	// app.devin.ai or api.devin.ai is the webapp or REST host, not the Connect RPC API server.
	if strings.Contains(clean, "devin.ai") {
		return devinDefaultAPIServerURL
	}
	return clean
}

// devinAuthHeader builds the "Basic <T>-<T>" authorization value Devin CLI
// sends: the windsurf API key string repeated twice.
func devinAuthHeader(token string) string {
	return "Basic " + token + "-" + token
}

// devinEncodeVarint appends a protobuf varint.
func devinEncodeVarint(out []byte, v uint64) []byte {
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			return append(out, b)
		}
	}
}

// devinEncodeField appends one protobuf field (key + raw payload).
func devinEncodeField(out []byte, num int, wire int, payload []byte) []byte {
	out = devinEncodeVarint(out, uint64(num<<3|wire))
	return append(out, payload...)
}

// devinEncodeString encodes a length-delimited string field payload.
func devinEncodeString(s string) []byte {
	b := []byte(s)
	return append(devinEncodeVarint(nil, uint64(len(b))), b...)
}

// devinClientContext builds the f1 client-context sub-message: static client
// identity plus the per-credential session token and the client fingerprint
// captured from Devin CLI traffic. The fingerprint is opaque to the server
// (byte replay succeeds), so a zero-value fingerprint is acceptable; the
// session token is what authenticates the request.
func devinClientContext(token, fingerprint string) []byte {
	var msg []byte
	msg = append(msg, devinEncodeField(nil, 1, 2, devinEncodeString(devinClientName))...)
	msg = append(msg, devinEncodeField(nil, 2, 2, devinEncodeString(devinClientVersion))...)
	msg = append(msg, devinEncodeField(nil, 3, 2, devinEncodeString(token))...)
	msg = append(msg, devinEncodeField(nil, 4, 2, devinEncodeString("en"))...)
	msg = append(msg, devinEncodeField(nil, 5, 2, devinEncodeString("linux"))...)
	msg = append(msg, devinEncodeField(nil, 7, 2, devinEncodeString(devinClientVersion))...)
	msg = append(msg, devinEncodeField(nil, 12, 2, devinEncodeString(devinClientKind))...)
	msg = append(msg, devinEncodeField(nil, 28, 2, devinEncodeString(devinClientKind))...)
	if fingerprint != "" {
		msg = append(msg, devinEncodeField(nil, 31, 2, devinEncodeString(fingerprint))...)
	}
	return msg
}

// devinUserBlock builds the f3 user-message block {f1=session UUID, f2=role,
// f3=text}. Fresh UUIDs are generated per request; execOnce callers reuse the
// same UUID so retries stay on one server-side session.
func devinUserBlock(sessionUUID, text string) []byte {
	var msg []byte
	msg = append(msg, devinEncodeField(nil, 1, 2, devinEncodeString(sessionUUID))...)
	msg = append(msg, devinEncodeField(nil, 2, 0, devinEncodeVarint(nil, 1))...)
	msg = append(msg, devinEncodeField(nil, 3, 2, devinEncodeString(text))...)
	return msg
}

// devinBuildChatRequest assembles a GetChatMessage unary request body
// (enveloped: 0x00 + u32be length + protobuf) from the captured schema.
func devinBuildChatRequest(token, modelUID, systemPrompt, userText, sessionUUID, fingerprint string, tools ...devinToolDef) []byte {
	var msg []byte
	// f1 is itself a length-delimited message.
	ctx := devinClientContext(token, fingerprint)
	msg = append(msg, devinEncodeField(nil, 1, 2, append(devinEncodeVarint(nil, uint64(len(ctx))), ctx...))...)
	msg = append(msg, devinEncodeField(nil, 2, 2, devinEncodeString(systemPrompt))...)
	user := devinUserBlock(sessionUUID, userText)
	msg = append(msg, devinEncodeField(nil, 3, 2, append(devinEncodeVarint(nil, uint64(len(user))), user...))...)
	msg = append(msg, devinEncodeField(nil, 7, 0, devinEncodeVarint(nil, 5))...)
	// Tool definitions ride f10, one repeated submessage per tool.
	for _, t := range tools {
		def := devinEncodeToolDef(t)
		msg = append(msg, devinEncodeField(nil, devinReqToolsField, 2, append(devinEncodeVarint(nil, uint64(len(def))), def...))...)
	}
	msg = append(msg, devinEncodeField(nil, 20, 0, devinEncodeVarint(nil, 1))...)
	msg = append(msg, devinEncodeField(nil, 21, 2, devinEncodeString(modelUID))...)
	out := []byte{0x00}
	var ln [4]byte
	binary.BigEndian.PutUint32(ln[:], uint32(len(msg)))
	out = append(out, ln[:]...)
	return append(out, msg...)
}

// devinDecodeFrames splits a Connect enveloped body into (data, trailer)
// payloads. Flag 0x00 frames carry response messages, 0x02 carries the
// terminal Connect trailer JSON.
func devinDecodeFrames(body []byte) (data [][]byte, trailer []byte, err error) {
	pos := 0
	for pos+5 <= len(body) {
		flag := body[pos]
		ln := int(binary.BigEndian.Uint32(body[pos+1 : pos+5]))
		if pos+5+ln > len(body) {
			return nil, nil, fmt.Errorf("devin: truncated connect frame at %d", pos)
		}
		payload := body[pos+5 : pos+5+ln]
		pos += 5 + ln
		// Bit 0x01 marks a gzip payload; bit 0x02 marks the trailer.
		if flag&devinConnectCompressedFlag != 0 {
			decoded, errGz := devinGunzip(payload)
			if errGz != nil {
				return nil, nil, fmt.Errorf("devin: gunzip frame: %w", errGz)
			}
			payload = decoded
		}
		if flag&devinConnectEndStreamFlag != 0 {
			trailer = payload
		} else {
			data = append(data, payload)
		}
	}
	return data, trailer, nil
}

// devinExtractTextDelta pulls the assistant answer delta (f3) out of one
// enveloped data frame. It returns ("", false) when the frame carries no
// answer text (timestamps, metadata-only, or reasoning-only frames).
//
// Devin streams two separate text channels: f3 carries the user-visible
// answer and f9 carries the model's private reasoning. Reading f9 as the
// answer leaked the chain-of-thought ("The user wants ... I'll provide
// that.") while discarding the real reply, so the two must stay distinct.
func devinExtractTextDelta(frame []byte) (string, bool) {
	return devinExtractFieldText(frame, devinAnswerField)
}

// devinExtractReasoningDelta pulls the reasoning delta (f9) out of a frame.
func devinExtractReasoningDelta(frame []byte) (string, bool) {
	return devinExtractFieldText(frame, devinReasoningField)
}

func devinExtractFieldText(frame []byte, want int) (string, bool) {
	pos := 0
	for pos < len(frame) {
		key, n := binary.Uvarint(frame[pos:])
		if n <= 0 {
			return "", false
		}
		pos += n
		num := int(key >> 3)
		wire := int(key & 7)
		switch wire {
		case 0:
			_, n := binary.Uvarint(frame[pos:])
			if n <= 0 {
				return "", false
			}
			pos += n
		case 1:
			pos += 8
		case 2:
			ln, n := binary.Uvarint(frame[pos:])
			if n <= 0 || pos+n+int(ln) > len(frame) {
				return "", false
			}
			raw := frame[pos+n : pos+n+int(ln)]
			pos += n + int(ln)
			if num == want {
				return string(raw), true
			}
		case 5:
			pos += 4
		default:
			return "", false
		}
		if pos > len(frame) {
			return "", false
		}
	}
	return "", false
}

// devinDoPost sends one enveloped Connect unary request.
func (e *DevinExecutor) devinDoPost(ctx context.Context, auth *cliproxyauth.Auth, model, url, token string, body []byte) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	framed, errGz := devinGzipFrame(body)
	if errGz == nil {
		req.Body = io.NopCloser(bytes.NewReader(framed))
		req.ContentLength = int64(len(framed))
		req.Header.Set("connect-content-encoding", "gzip")
	}
	req.Header.Set("content-type", devinConnectProtoContentType)
	req.Header.Set("connect-protocol-version", devinConnectProtocolVersion)
	req.Header.Set("authorization", devinAuthHeader(token))
	req.Header.Set("accept", "*/*")
	req.Header.Set("connect-accept-encoding", "gzip")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, devinMaxNonStreamBytes+1<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		devinLogUpstreamFailure(ctx, auth, model, resp.StatusCode, body, respBody)
		return nil, nil, fmt.Errorf("devin: upstream status %d: %s", resp.StatusCode, truncateDevinErr(respBody))
	}
	return respBody, resp.Header.Clone(), nil
}

// devinRequestText flattens an OpenAI-style conversation into the single
// prompt Devin's Connect RPC accepts, plus the joined system prompt.
//
// Devin takes exactly one user string, so a multi-turn chat must be rendered
// as a transcript. Assigning text = m.content per user message (the previous
// behaviour) kept only the final user turn, so earlier turns and every
// assistant reply were silently dropped -- a trailing notice block would
// replace the user's real question and the model answered as if it had
// received nothing.
func devinRequestText(req cliproxyexecutor.Request) (text, system string) {
	payload := req.Payload
	if len(payload) == 0 {
		return "", ""
	}
	messages := extractDevinMessages(payload)

	var systems []string
	var turns []devinMessage
	for _, m := range messages {
		content := strings.TrimSpace(m.content)
		if content == "" {
			continue
		}
		if m.role == "system" || m.role == "developer" {
			systems = append(systems, content)
			continue
		}
		turns = append(turns, devinMessage{role: m.role, content: content})
	}
	system = strings.Join(systems, "\n\n")

	// A plain single-turn prompt is forwarded verbatim so simple requests are
	// not decorated with role labels.
	if len(turns) == 1 && turns[0].role == "user" {
		return turns[0].content, system
	}

	var sb strings.Builder
	for i, m := range turns {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		switch m.role {
		case "assistant":
			sb.WriteString("Assistant: ")
		case "tool", "function":
			sb.WriteString("Tool result: ")
		default:
			sb.WriteString("User: ")
		}
		sb.WriteString(m.content)
	}
	return sb.String(), system
}

type devinMessage struct {
	role          string
	content       string
	toolCallID    string
	toolCalls     []devinToolCall
	thinking      string
	signature     string
	signatureType string
	toolIsError   bool
}

func extractDevinMessages(payload []byte) []devinMessage {
	var doc struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			Reasoning  string `json:"reasoning_content"`
			Thinking   string `json:"thinking"`
			Signature  string `json:"thinking_signature"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil
	}
	out := make([]devinMessage, 0, len(doc.Messages))
	for _, m := range doc.Messages {
		msg := devinMessage{role: m.Role, content: devinContentToString(m.Content), toolCallID: m.ToolCallID}
		msg.thinking = m.Reasoning
		if msg.thinking == "" {
			msg.thinking = m.Thinking
		}
		msg.signature = m.Signature
		for _, tc := range m.ToolCalls {
			msg.toolCalls = append(msg.toolCalls, devinToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
		out = append(out, msg)
	}
	return out
}

func devinContentToString(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, part := range v {
			if pm, ok := part.(map[string]any); ok {
				if t, _ := pm["text"].(string); t != "" {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	default:
		return ""
	}
}

// devinNewSessionUUID mints a fresh v4 session UUID per request.
func devinNewSessionUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		uint64(b[10])<<40|uint64(b[11])<<32|uint64(b[12])<<24|uint64(b[13])<<16|uint64(b[14])<<8|uint64(b[15]))
}

func truncateDevinErr(body []byte) string {
	const maxErrBody = 512
	if len(body) > maxErrBody {
		body = body[:maxErrBody]
	}
	return strings.TrimSpace(string(body))
}

// devinMaskSecrets redacts the credential material that appears in Connect
// error bodies and echoed request payloads before it reaches logs.
func devinMaskSecrets(s string) string {
	// Mask the "Basic <token>-<token>" credential form.
	if idx := strings.Index(s, "Basic "); idx >= 0 {
		end := idx + len("Basic ")
		for end < len(s) && s[end] != '\n' && s[end] != '"' && s[end] != ' ' {
			end++
		}
		s = s[:idx] + "Basic [redacted]" + s[end:]
	}
	// Mask opaque bearer/JWT runs wherever else they appear.
	return devinLongTokenRe.ReplaceAllString(s, "[redacted]")
}

var devinLongTokenRe = regexp.MustCompile(`(?:devin-session-token\$)?eyJ[A-Za-z0-9._\-]{20,}`)

// devinLogUpstreamFailure emits the masked request/response detail callers
// need to debug 4xx/5xx without leaking credentials.
func devinLogUpstreamFailure(_ context.Context, auth *cliproxyauth.Auth, model string, status int, reqBody, respBody []byte) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	log.WithFields(log.Fields{
		"provider":        "devin",
		"auth_id":         authID,
		"resolved_model":  model,
		"upstream_status": status,
		"raw_request":     devinMaskSecrets(truncateDevinErr(reqBody)),
		"raw_response":    devinMaskSecrets(truncateDevinErr(respBody)),
	}).Debug("devin: upstream returned an error status")
}

// devinOpenAIChunk renders one OpenAI Chat Completions SSE chunk carrying a
// content delta, or a [DONE] terminator when done is true.
func devinOpenAIChunk(model, delta string, done bool) []byte {
	if done {
		return []byte("data: [DONE]\n\n")
	}
	escaped, _ := marshalDevinJSONString(delta)
	return []byte(fmt.Sprintf("data: {\"id\":\"chatcmpl-devin\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":%s,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%s},\"finish_reason\":null}]}\n\n", marshalDevinJSONStringOrEmpty(model), escaped))
}

func marshalDevinJSONString(s string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString("\\\"")
		case '\\':
			buf.WriteString("\\\\")
		case '\n':
			buf.WriteString("\\n")
		case '\r':
			buf.WriteString("\\r")
		case '\t':
			buf.WriteString("\\t")
		default:
			if r < 0x20 {
				fmt.Fprintf(&buf, "\\u%04x", r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
	return buf.Bytes(), nil
}

func marshalDevinJSONStringOrEmpty(s string) string {
	b, _ := marshalDevinJSONString(s)
	return string(b)
}

// Execute runs a non-streaming Devin request: it sends GetChatMessage,
// accumulates enveloped f9 text deltas, and returns one OpenAI-style
// chat.completion payload.
func (e *DevinExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	token := devinAPIKey(auth)
	if token == "" {
		return cliproxyexecutor.Response{}, fmt.Errorf("devin: missing API key")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return cliproxyexecutor.Response{}, fmt.Errorf("devin: missing model")
	}
	msgs, system := devinRequestMessages(req.Payload)
	if len(msgs) == 0 {
		// Fall back to the flattened transcript when no structured turns parse.
		text, sys := devinRequestText(req)
		if system == "" {
			system = sys
		}
		msgs = []devinMessage{{role: "user", content: text}}
	}
	sessionUUID := devinNewSessionUUID()
	spec := devinChatSpec{
		Token:       token,
		ModelUID:    model,
		System:      system,
		Messages:    msgs,
		SessionUUID: sessionUUID,
		CascadeID:   sessionUUID,
		PromptID:    devinNewSessionUUID(),
		Tools:       devinExtractTools(req.Payload),
		Config:      devinExtractCompletionConfig(req.Payload),
		ExecutionID: devinNewSessionUUID(),
	}
	// A router uid must be exchanged for a concrete model first.
	if devinIsRouterModel(model) {
		if assignment, ok := e.devinAssignModel(ctx, auth, spec, model); ok {
			if assignment.ModelUID != "" {
				spec.ModelUID = assignment.ModelUID
			}
			spec.AssignmentJWT = assignment.JWT
		}
	}
	body := devinBuildChatRequestFull(spec)
	respBody, headers, err := e.devinDoPost(ctx, auth, model, devinServerURL(auth)+devinGetChatMessagePath, token, body)
	if err != nil {
		log.WithFields(log.Fields{"provider": "devin", "model": model}).WithError(err).Debug("devin: upstream request failed")
		return cliproxyexecutor.Response{}, err
	}
	frames, trailer, err := devinDecodeFrames(respBody)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if msg := devinTrailerError(trailer); msg != "" {
		log.WithFields(log.Fields{"provider": "devin", "model": model}).
			Warnf("devin: upstream request rejected [%s]", devinRequestShape(spec, body, len(frames)))
		return cliproxyexecutor.Response{}, fmt.Errorf("devin: %s", msg)
	}
	if len(frames) == 0 {
		return cliproxyexecutor.Response{}, fmt.Errorf("devin: no data frames: %s", devinTrailerMessage(trailer))
	}
	var sb strings.Builder
	var reasoning strings.Builder
	finish := "stop"
	var usage devinUsage
	calls := make([]devinToolCall, 0, 2)
	byID := make(map[string]int, 2)
	for _, frame := range frames {
		if delta, ok := devinExtractTextDelta(frame); ok {
			sb.WriteString(delta)
		}
		if call, ok := devinExtractToolCallDelta(frame); ok {
			devinMergeToolCall(&calls, byID, call)
		}
		if reason, ok := devinExtractFinishReason(frame); ok {
			finish = reason
		}
		if u, ok := devinExtractUsage(frame); ok {
			usage = u
		}
		if r, ok := devinExtractReasoningDelta(frame); ok {
			reasoning.WriteString(r)
		}
		devinExtractCost(frame, &usage)
	}
	if len(calls) > 0 && finish == "stop" {
		finish = "tool_calls"
	}
	content := sb.String()
	payload := devinBuildCompletionPayload(model, content, reasoning.String(), calls, finish, usage)
	return cliproxyexecutor.Response{Payload: payload, Headers: headers}, nil
}

func mustMarshalDevinJSON(s string) string {
	b, _ := marshalDevinJSONString(s)
	return string(b)
}

// ExecuteStream runs a streaming Devin request. Devin's GetChatMessage
// returns one enveloped stream per call, so the executor issues a single
// upstream call and replays each f9 delta as an OpenAI SSE chunk.
func (e *DevinExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	token := devinAPIKey(auth)
	if token == "" {
		return nil, fmt.Errorf("devin: missing API key")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, fmt.Errorf("devin: missing model")
	}
	msgs, system := devinRequestMessages(req.Payload)
	if len(msgs) == 0 {
		// Fall back to the flattened transcript when no structured turns parse.
		text, sys := devinRequestText(req)
		if system == "" {
			system = sys
		}
		msgs = []devinMessage{{role: "user", content: text}}
	}
	sessionUUID := devinNewSessionUUID()
	spec := devinChatSpec{
		Token:       token,
		ModelUID:    model,
		System:      system,
		Messages:    msgs,
		SessionUUID: sessionUUID,
		CascadeID:   sessionUUID,
		PromptID:    devinNewSessionUUID(),
		Tools:       devinExtractTools(req.Payload),
		Config:      devinExtractCompletionConfig(req.Payload),
		ExecutionID: devinNewSessionUUID(),
	}
	// A router uid must be exchanged for a concrete model first.
	if devinIsRouterModel(model) {
		if assignment, ok := e.devinAssignModel(ctx, auth, spec, model); ok {
			if assignment.ModelUID != "" {
				spec.ModelUID = assignment.ModelUID
			}
			spec.AssignmentJWT = assignment.JWT
		}
	}
	body := devinBuildChatRequestFull(spec)
	resp, err := e.devinOpenStream(ctx, auth, model, devinServerURL(auth)+devinGetChatMessagePath, token, body)
	if err != nil {
		log.WithFields(log.Fields{"provider": "devin", "model": model}).WithError(err).Debug("devin: upstream stream request failed")
		return nil, err
	}
	headers := resp.Header.Clone()
	chunks := make(chan cliproxyexecutor.StreamChunk, 64)
	go func() {
		defer close(chunks)
		send := func(p []byte) bool {
			select {
			case chunks <- cliproxyexecutor.StreamChunk{Payload: p}:
				return true
			case <-ctx.Done():
				return false
			}
		}
		finish := "stop"
		var usage devinUsage
		calls := make([]devinToolCall, 0, 2)
		byID := make(map[string]int, 2)
		defer func() { _ = resp.Body.Close() }()
		// Emit each frame as it arrives so throughput and time-to-first-token
		// reflect the real generation instead of one replay burst.
		dataFrames := 0
		var trailer []byte
		streamErr := devinStreamFrames(resp.Body, func(frame []byte, isTrailer bool) error {
			if isTrailer {
				trailer = append(trailer[:0], frame...)
				return nil
			}
			dataFrames++
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if delta, ok := devinExtractTextDelta(frame); ok && delta != "" {
				if !send(devinOpenAIChunk(model, delta, false)) {
					return context.Canceled
				}
			}
			if call, ok := devinExtractToolCallDelta(frame); ok {
				devinMergeToolCall(&calls, byID, call)
			}
			if reason, ok := devinExtractFinishReason(frame); ok {
				finish = reason
			}
			if u, ok := devinExtractUsage(frame); ok {
				usage = u
			}
			devinExtractCost(frame, &usage)
			return nil
		})
		// Surface upstream failures instead of closing the stream as if it
		// succeeded. A silently empty stream reaches the client as a missing
		// terminal event, whose wording differs per client format, and hides
		// the Connect trailer that explains the rejection.
		if failure := devinStreamFailure(trailer, streamErr, dataFrames); failure != nil {
			// Opaque invalid_argument rejections carry no HTTP body, so the
			// request shape is the only evidence available for diagnosis.
			log.WithFields(log.Fields{"provider": "devin", "model": model}).WithError(failure).
				Warnf("devin: upstream stream rejected [%s]", devinRequestShape(spec, body, dataFrames))
			select {
			case chunks <- cliproxyexecutor.StreamChunk{Err: failure}:
			case <-ctx.Done():
			}
			return
		}
		if len(calls) > 0 {
			if finish == "stop" {
				finish = "tool_calls"
			}
			if !send(devinToolCallChunk(model, calls)) {
				return
			}
		}
		if !send(devinFinishChunk(model, finish, usage)) {
			return
		}
		send(devinOpenAIChunk(model, "", true))
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: chunks}, nil
}

// devinAssignModel resolves a model-router uid via the AssignModel RPC.
//
// Router uids (thinking-effort aliases) must be exchanged for a concrete model
// before GetChatMessage; the returned jwt is echoed back on the chat request.
func (e *DevinExecutor) devinAssignModel(ctx context.Context, auth *cliproxyauth.Auth, spec devinChatSpec, routerUID string) (devinModelAssignment, bool) {
	body := devinBuildAssignModelRequest(spec, routerUID)
	respBody, _, err := e.devinDoPost(ctx, auth, routerUID, devinServerURL(auth)+devinAssignModelPath, spec.Token, body)
	if err != nil {
		log.WithFields(log.Fields{"provider": "devin", "router": routerUID}).WithError(err).Debug("devin: AssignModel failed; using the requested model")
		return devinModelAssignment{}, false
	}
	frames, _, err := devinDecodeFrames(respBody)
	if err != nil {
		return devinModelAssignment{}, false
	}
	return devinParseAssignModelResponse(frames)
}

// devinIsRouterModel reports whether a model id routes through AssignModel.
// Thinking-effort suffixes are resolved server side.
func devinIsRouterModel(model string) bool {
	return strings.HasSuffix(model, "-router") || strings.Contains(model, "model-router")
}

// devinStreamFailure classifies the end of an upstream stream.
//
// A Connect trailer carrying an error, a transport failure, or a stream that
// produced no data frames all mean the turn failed. Reporting them as a clean
// close leaves the client without a terminal event.
func devinStreamFailure(trailer []byte, streamErr error, dataFrames int) error {
	if msg := devinTrailerError(trailer); msg != "" {
		return devinClassifyTrailerError(msg)
	}
	if streamErr != nil && !errors.Is(streamErr, context.Canceled) {
		return fmt.Errorf("devin: stream failed: %w", streamErr)
	}
	if dataFrames == 0 && streamErr == nil {
		return fmt.Errorf("devin: upstream produced no data frames")
	}
	return nil
}

// devinTrailerError returns a readable message when a Connect trailer carries
// an error, or an empty string for a clean trailer.
func devinTrailerError(trailer []byte) string {
	trimmed := bytes.TrimSpace(trailer)
	if len(trimmed) == 0 || string(trimmed) == "{}" {
		return ""
	}
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return ""
	}
	if doc.Error.Code == "" && doc.Error.Message == "" {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(doc.Error.Code+": "+doc.Error.Message, ": "))
}

// devinRequestShape renders the request shape behind an upstream rejection.
//
// Opaque invalid_argument trailers carry no HTTP body, so the request shape
// is the only evidence available. The log formatter renders fields from an
// allowlist, so this goes into the message itself.
func devinRequestShape(spec devinChatSpec, framed []byte, dataFrames int) string {
	toolNames := make([]string, 0, len(spec.Tools))
	schemaBytes := 0
	for _, t := range spec.Tools {
		toolNames = append(toolNames, t.Name)
		schemaBytes += len(t.Parameters)
	}
	roles := make([]string, 0, len(spec.Messages))
	promptBytes, toolCalls, toolResults, thinkingTurns := 0, 0, 0, 0
	for _, m := range spec.Messages {
		roles = append(roles, m.role)
		promptBytes += len(m.content)
		toolCalls += len(m.toolCalls)
		if m.toolCallID != "" {
			toolResults++
		}
		if m.thinking != "" {
			thinkingTurns++
		}
	}
	return fmt.Sprintf(
		"framed=%dB system=%dB prompt=%dB msgs=%d roles=%s tools=%d tool_schema=%dB tool_calls=%d tool_results=%d thinking=%d frames=%d names=%s",
		len(framed), len(spec.System), promptBytes, len(spec.Messages),
		strings.Join(roles, ","), len(spec.Tools), schemaBytes,
		toolCalls, toolResults, thinkingTurns, dataFrames, strings.Join(toolNames, ","))
}

// devinRequestScopedCodes are the Connect codes that describe the request
// rather than the credential behind it.
//
// invalid_argument is the important one: it arrives intermittently for
// requests whose shape has been reproduced as working, so it is an upstream
// fault for that attempt, not evidence that the credential is unusable.
// Treating it as a credential fault took the only devin credential out of
// rotation, and every request that followed during the cooldown reported
// auth_unavailable instead of reaching devin at all.
var devinRequestScopedCodes = []string{"invalid_argument", "failed_precondition", "out_of_range"}

// devinClassifyTrailerError converts a Connect trailer message into an error
// whose status code tells the cooldown logic whether the credential is at
// fault.
func devinClassifyTrailerError(msg string) error {
	lower := strings.ToLower(msg)
	for _, code := range devinRequestScopedCodes {
		if strings.HasPrefix(lower, code) {
			// A 400 marks this as a request-shape fault, which the credential
			// cooldown path skips.
			return &devinStatusError{status: http.StatusBadRequest, msg: "devin: " + msg}
		}
	}
	return fmt.Errorf("devin: %s", msg)
}

// devinStatusError carries an HTTP status alongside the upstream message so the
// cooldown logic can classify the failure.
type devinStatusError struct {
	status int
	msg    string
}

// Error renders the upstream message.
func (e *devinStatusError) Error() string { return e.msg }

// StatusCode reports the HTTP status this failure maps to.
func (e *devinStatusError) StatusCode() int { return e.status }

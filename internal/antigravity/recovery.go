package antigravity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// RecoveryResumeText is sent when auto-resuming a repaired session.
	RecoveryResumeText = "[session recovered - continuing previous task]"

	cancelledToolResultContent = "Operation cancelled by user (ESC pressed)"
	thinkingPrependPartID      = "prt_0000000000_thinking"
)

// RecoveryErrorType identifies recoverable Antigravity/OpenCode session errors.
type RecoveryErrorType string

const (
	// RecoveryErrorTypeToolResultMissing is raised when a tool_use has no matching tool_result.
	RecoveryErrorTypeToolResultMissing RecoveryErrorType = "tool_result_missing"
	// RecoveryErrorTypeThinkingBlockOrder is raised when thinking parts are missing or out of order.
	RecoveryErrorTypeThinkingBlockOrder RecoveryErrorType = "thinking_block_order"
	// RecoveryErrorTypeThinkingDisabledViolation is raised when thinking appears for a non-thinking model.
	RecoveryErrorTypeThinkingDisabledViolation RecoveryErrorType = "thinking_disabled_violation"
)

var messageIndexPattern = regexp.MustCompile(`messages\.(\d+)`)
var thinkingTypes = map[string]struct{}{"thinking": {}, "redacted_thinking": {}, "reasoning": {}}
var metaTypes = map[string]struct{}{"step-start": {}, "step-finish": {}}

// MessagePart is the SDK-facing message part shape used for recovery decisions.
type MessagePart struct {
	Type     string         `json:"type"`
	ID       string         `json:"id,omitempty"`
	Text     string         `json:"text,omitempty"`
	Thinking string         `json:"thinking,omitempty"`
	Name     string         `json:"name,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
	CallID   string         `json:"callID,omitempty"`
	Tool     string         `json:"tool,omitempty"`
	State    *ToolState     `json:"state,omitempty"`
}

// ToolState is the stored OpenCode tool execution state used when rebuilding tool_use parts.
type ToolState struct {
	Status string         `json:"status,omitempty"`
	Input  map[string]any `json:"input,omitempty"`
	Output string         `json:"output,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// MessageData is the SDK-facing message shape used by session recovery.
type MessageData struct {
	Info  *MessageInfo  `json:"info,omitempty"`
	Parts []MessagePart `json:"parts,omitempty"`
}

// MessageInfo is the SDK-facing message metadata used by session recovery.
type MessageInfo struct {
	ID        string         `json:"id,omitempty"`
	Role      string         `json:"role,omitempty"`
	SessionID string         `json:"sessionID,omitempty"`
	ParentID  string         `json:"parentID,omitempty"`
	Error     any            `json:"error,omitempty"`
	Agent     string         `json:"agent,omitempty"`
	Model     map[string]any `json:"model,omitempty"`
}

// ResumeConfig captures the session, agent, and model values needed to resume after repair.
type ResumeConfig struct {
	SessionID string         `json:"sessionID"`
	Agent     string         `json:"agent,omitempty"`
	Model     map[string]any `json:"model,omitempty"`
}

// ToolResultPart is a synthetic tool_result part injected for interrupted tool calls.
type ToolResultPart struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

// StoredMessageMeta is OpenCode's on-disk message metadata shape.
type StoredMessageMeta struct {
	ID        string             `json:"id"`
	SessionID string             `json:"sessionID,omitempty"`
	Role      string             `json:"role"`
	ParentID  string             `json:"parentID,omitempty"`
	Time      *StoredMessageTime `json:"time,omitempty"`
	Error     any                `json:"error,omitempty"`
}

// StoredMessageTime stores OpenCode message timestamps.
type StoredMessageTime struct {
	Created   int64 `json:"created,omitempty"`
	Completed int64 `json:"completed,omitempty"`
}

// StoredPart is the on-disk OpenCode part shape needed by recovery.
type StoredPart struct {
	ID        string     `json:"id"`
	SessionID string     `json:"sessionID,omitempty"`
	MessageID string     `json:"messageID,omitempty"`
	Type      string     `json:"type"`
	Text      string     `json:"text,omitempty"`
	Thinking  string     `json:"thinking,omitempty"`
	CallID    string     `json:"callID,omitempty"`
	Tool      string     `json:"tool,omitempty"`
	State     *ToolState `json:"state,omitempty"`
	Synthetic bool       `json:"synthetic,omitempty"`
	Ignored   bool       `json:"ignored,omitempty"`
}

// GetErrorMessage extracts a normalized lower-case error message string from an arbitrary error value.
func GetErrorMessage(errorValue any) string {
	if errorValue == nil {
		return ""
	}
	if v, ok := errorValue.(string); ok {
		return strings.ToLower(v)
	}
	if e, ok := errorValue.(error); ok {
		return strings.ToLower(e.Error())
	}
	if m := messageFromErrorObject(errorValue); m != "" {
		return strings.ToLower(m)
	}
	b, e := json.Marshal(errorValue)
	if e != nil {
		return ""
	}
	return strings.ToLower(string(b))
}
func messageFromErrorObject(errorValue any) string {
	obj, ok := asMap(errorValue)
	if !ok {
		return ""
	}
	paths := []any{obj["data"], obj["error"], obj}
	if data, ok := asMap(obj["data"]); ok {
		paths = append(paths, data["error"])
	} else {
		paths = append(paths, nil)
	}
	for _, c := range paths {
		if m, ok := asMap(c); ok {
			if msg, ok := m["message"].(string); ok && msg != "" {
				return msg
			}
		}
	}
	return ""
}
func asMap(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	switch v := value.(type) {
	case map[string]any:
		return v, true
	default:
		return nil, false
	}
}

// ExtractMessageIndex extracts the numeric index from an error message such as "messages.79".
func ExtractMessageIndex(errorValue any) *int {
	msg := GetErrorMessage(errorValue)
	m := messageIndexPattern.FindStringSubmatch(msg)
	if len(m) < 2 {
		return nil
	}
	v, e := strconv.Atoi(m[1])
	if e != nil {
		return nil
	}
	return &v
}

// DetectErrorType detects the reference recoverable error type from an error object.
func DetectErrorType(errorValue any) *RecoveryErrorType {
	message := GetErrorMessage(errorValue)
	expectedFound := (strings.Contains(message, "expected thinking") || strings.Contains(message, "expected a thinking")) && strings.Contains(message, "found")
	if strings.Contains(message, "tool_use") && strings.Contains(message, "tool_result") {
		v := RecoveryErrorTypeToolResultMissing
		return &v
	}
	if strings.Contains(message, "thinking") && (strings.Contains(message, "first block") || strings.Contains(message, "must start with") || strings.Contains(message, "preceeding") || strings.Contains(message, "preceding") || expectedFound) {
		v := RecoveryErrorTypeThinkingBlockOrder
		return &v
	}
	if strings.Contains(message, "thinking is disabled") && strings.Contains(message, "cannot contain") {
		v := RecoveryErrorTypeThinkingDisabledViolation
		return &v
	}
	return nil
}

// IsRecoverableError reports whether an error can be repaired by session recovery.
func IsRecoverableError(errorValue any) bool { return DetectErrorType(errorValue) != nil }

// ExtractToolUseIDs returns tool_use part IDs that need synthetic tool_result parts.
func ExtractToolUseIDs(parts []MessagePart) []string {
	ids := []string{}
	for _, p := range parts {
		if p.Type == "tool_use" && p.ID != "" {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// BuildCancelledToolResults creates synthetic tool_result parts for tool_result_missing recovery.
func BuildCancelledToolResults(parts []MessagePart) []ToolResultPart {
	ids := ExtractToolUseIDs(parts)
	out := make([]ToolResultPart, 0, len(ids))
	for _, id := range ids {
		out = append(out, ToolResultPart{Type: "tool_result", ToolUseID: id, Content: cancelledToolResultContent})
	}
	return out
}

// NormalizeStoredPartsForToolUse converts stored OpenCode tool parts to SDK-style tool_use parts.
func NormalizeStoredPartsForToolUse(parts []StoredPart) []MessagePart {
	out := make([]MessagePart, 0, len(parts))
	for _, p := range parts {
		typ := p.Type
		if typ == "tool" {
			typ = "tool_use"
		}
		id := p.ID
		if p.CallID != "" {
			id = p.CallID
		}
		input := map[string]any(nil)
		if p.State != nil {
			input = p.State.Input
		}
		out = append(out, MessagePart{Type: typ, ID: id, Name: p.Tool, Input: input})
	}
	return out
}

// DefaultOpenCodeStorage returns OpenCode's default XDG-backed message and part storage paths.
func DefaultOpenCodeStorage() OpenCodeStorage {
	storage := filepath.Join(xdgDataHome(), "opencode", "storage")
	return OpenCodeStorage{MessageStorage: filepath.Join(storage, "message"), PartStorage: filepath.Join(storage, "part")}
}

// OpenCodeStorage provides filesystem repair helpers for OpenCode session data.
type OpenCodeStorage struct {
	MessageStorage string
	PartStorage    string
}

// GetMessageDir finds the message directory for a session, checking direct and nested layouts.
func (s OpenCodeStorage) GetMessageDir(sessionID string) string {
	if s.MessageStorage == "" || !pathExists(s.MessageStorage) {
		return ""
	}
	direct := filepath.Join(s.MessageStorage, sessionID)
	if pathExists(direct) {
		return direct
	}
	entries, e := os.ReadDir(s.MessageStorage)
	if e != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p := filepath.Join(s.MessageStorage, entry.Name(), sessionID)
		if pathExists(p) {
			return p
		}
	}
	return ""
}

// ReadMessages reads and sorts session messages by creation time, then ID.
func (s OpenCodeStorage) ReadMessages(sessionID string) []StoredMessageMeta {
	dir := s.GetMessageDir(sessionID)
	if dir == "" || !pathExists(dir) {
		return []StoredMessageMeta{}
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return []StoredMessageMeta{}
	}
	msgs := []StoredMessageMeta{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			continue
		}
		var m StoredMessageMeta
		if json.Unmarshal(b, &m) == nil {
			msgs = append(msgs, m)
		}
	}
	sort.Slice(msgs, func(i, j int) bool {
		ai, bi := int64(0), int64(0)
		if msgs[i].Time != nil {
			ai = msgs[i].Time.Created
		}
		if msgs[j].Time != nil {
			bi = msgs[j].Time.Created
		}
		if ai != bi {
			return ai < bi
		}
		return msgs[i].ID < msgs[j].ID
	})
	return msgs
}

// ReadParts reads all JSON parts for a message.
func (s OpenCodeStorage) ReadParts(messageID string) []StoredPart {
	dir := filepath.Join(s.PartStorage, messageID)
	if !pathExists(dir) {
		return []StoredPart{}
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return []StoredPart{}
	}
	parts := []StoredPart{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			continue
		}
		var p StoredPart
		if json.Unmarshal(b, &p) == nil {
			parts = append(parts, p)
		}
	}
	return parts
}

// HasContent reports whether a part is non-thinking, non-meta user-visible content.
func HasContent(part StoredPart) bool {
	if _, ok := thinkingTypes[part.Type]; ok {
		return false
	}
	if _, ok := metaTypes[part.Type]; ok {
		return false
	}
	if part.Type == "text" {
		return strings.TrimSpace(part.Text) != ""
	}
	return part.Type == "tool" || part.Type == "tool_use" || part.Type == "tool_result"
}

// MessageHasContent reports whether any part for the message has recoverable content.
func (s OpenCodeStorage) MessageHasContent(messageID string) bool {
	for _, p := range s.ReadParts(messageID) {
		if HasContent(p) {
			return true
		}
	}
	return false
}

// GeneratePartID generates a synthetic text part ID using prt_<hex timestamp><random>.
func GeneratePartID() string {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 16)
	jitterRand.Lock()
	rv := jitterRand.r.Int63()
	jitterRand.Unlock()
	random := strconv.FormatInt(rv, 36)
	if len(random) > 8 {
		random = random[:8]
	}
	return "prt_" + ts + random
}

// InjectTextPart writes a synthetic text part into a message's part directory.
func (s OpenCodeStorage) InjectTextPart(sessionID, messageID, text string) bool {
	dir := filepath.Join(s.PartStorage, messageID)
	if os.MkdirAll(dir, 0o755) != nil {
		return false
	}
	id := GeneratePartID()
	part := StoredPart{ID: id, SessionID: sessionID, MessageID: messageID, Type: "text", Text: text, Synthetic: true}
	return writeJSON(filepath.Join(dir, id+".json"), part)
}

// FindMessagesWithThinkingBlocks returns assistant messages containing thinking blocks.
func (s OpenCodeStorage) FindMessagesWithThinkingBlocks(sessionID string) []string {
	out := []string{}
	for _, m := range s.ReadMessages(sessionID) {
		if m.Role != "assistant" {
			continue
		}
		for _, p := range s.ReadParts(m.ID) {
			if _, ok := thinkingTypes[p.Type]; ok {
				out = append(out, m.ID)
				break
			}
		}
	}
	return out
}

// FindMessagesWithThinkingOnly returns assistant messages that have thinking but no content.
func (s OpenCodeStorage) FindMessagesWithThinkingOnly(sessionID string) []string {
	out := []string{}
	for _, m := range s.ReadMessages(sessionID) {
		if m.Role != "assistant" {
			continue
		}
		parts := s.ReadParts(m.ID)
		if len(parts) == 0 {
			continue
		}
		hasThinking, hasContent := false, false
		for _, p := range parts {
			if _, ok := thinkingTypes[p.Type]; ok {
				hasThinking = true
			}
			if HasContent(p) {
				hasContent = true
			}
		}
		if hasThinking && !hasContent {
			out = append(out, m.ID)
		}
	}
	return out
}

// FindMessagesWithOrphanThinking returns assistant messages whose first sorted part is not thinking.
func (s OpenCodeStorage) FindMessagesWithOrphanThinking(sessionID string) []string {
	out := []string{}
	for _, m := range s.ReadMessages(sessionID) {
		if m.Role != "assistant" {
			continue
		}
		parts := s.ReadParts(m.ID)
		if len(parts) == 0 {
			continue
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].ID < parts[j].ID })
		if _, ok := thinkingTypes[parts[0].Type]; !ok {
			out = append(out, m.ID)
		}
	}
	return out
}

// PrependThinkingPart writes the deterministic synthetic thinking part used to repair block ordering.
func (s OpenCodeStorage) PrependThinkingPart(sessionID, messageID string) bool {
	dir := filepath.Join(s.PartStorage, messageID)
	if os.MkdirAll(dir, 0o755) != nil {
		return false
	}
	part := StoredPart{ID: thinkingPrependPartID, SessionID: sessionID, MessageID: messageID, Type: "thinking", Thinking: "", Synthetic: true}
	return writeJSON(filepath.Join(dir, thinkingPrependPartID+".json"), part)
}

// StripThinkingParts removes thinking, redacted_thinking, and reasoning parts from a message.
func (s OpenCodeStorage) StripThinkingParts(messageID string) bool {
	dir := filepath.Join(s.PartStorage, messageID)
	if !pathExists(dir) {
		return false
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return false
	}
	removed := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, e := os.ReadFile(path)
		if e != nil {
			continue
		}
		var p StoredPart
		if json.Unmarshal(b, &p) != nil {
			continue
		}
		if _, ok := thinkingTypes[p.Type]; ok {
			if e := os.Remove(path); e == nil || errors.Is(e, os.ErrNotExist) {
				removed = true
			}
		}
	}
	return removed
}

// FindMessageByIndexNeedingThinking returns the indexed assistant message if first part is not thinking.
func (s OpenCodeStorage) FindMessageByIndexNeedingThinking(sessionID string, targetIndex int) string {
	msgs := s.ReadMessages(sessionID)
	if targetIndex < 0 || targetIndex >= len(msgs) {
		return ""
	}
	m := msgs[targetIndex]
	if m.Role != "assistant" {
		return ""
	}
	parts := s.ReadParts(m.ID)
	if len(parts) == 0 {
		return ""
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].ID < parts[j].ID })
	if _, ok := thinkingTypes[parts[0].Type]; !ok {
		return m.ID
	}
	return ""
}

// RepairThinkingBlockOrder repairs thinking_block_order errors by prepending synthetic thinking parts.
func (s OpenCodeStorage) RepairThinkingBlockOrder(sessionID string, errorValue any) bool {
	if idx := ExtractMessageIndex(errorValue); idx != nil {
		if id := s.FindMessageByIndexNeedingThinking(sessionID, *idx); id != "" {
			return s.PrependThinkingPart(sessionID, id)
		}
	}
	orphans := s.FindMessagesWithOrphanThinking(sessionID)
	if len(orphans) == 0 {
		return false
	}
	ok := false
	for _, id := range orphans {
		if s.PrependThinkingPart(sessionID, id) {
			ok = true
		}
	}
	return ok
}

// RepairThinkingBlockOrder repairs thinking_block_order errors in default OpenCode storage.
func RepairThinkingBlockOrder(sessionID string, errorValue any) bool {
	return DefaultOpenCodeStorage().RepairThinkingBlockOrder(sessionID, errorValue)
}

// RepairThinkingDisabledViolation strips thinking parts from all assistant messages in default storage.
func RepairThinkingDisabledViolation(sessionID string) bool {
	s := DefaultOpenCodeStorage()
	msgs := s.FindMessagesWithThinkingBlocks(sessionID)
	if len(msgs) == 0 {
		return false
	}
	ok := false
	for _, id := range msgs {
		if s.StripThinkingParts(id) {
			ok = true
		}
	}
	return ok
}

// FindLastUserMessage returns the latest user message from a message slice.
func FindLastUserMessage(messages []MessageData) *MessageData {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Info != nil && messages[i].Info.Role == "user" {
			return &messages[i]
		}
	}
	return nil
}

// ExtractResumeConfig builds a resume config from the last user message and session ID.
func ExtractResumeConfig(userMessage *MessageData, sessionID string) ResumeConfig {
	cfg := ResumeConfig{SessionID: sessionID}
	if userMessage != nil && userMessage.Info != nil {
		cfg.Agent = userMessage.Info.Agent
		cfg.Model = userMessage.Info.Model
	}
	return cfg
}

// RecoveryToastContent contains a recovery toast title and message.
type RecoveryToastContent struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

// GetRecoveryToastContent returns the reference warning toast content for a recovery type.
func GetRecoveryToastContent(errorType *RecoveryErrorType) RecoveryToastContent {
	if errorType == nil {
		return RecoveryToastContent{"Session Recovery", "Attempting to recover session..."}
	}
	switch *errorType {
	case RecoveryErrorTypeToolResultMissing:
		return RecoveryToastContent{"Tool Crash Recovery", "Injecting cancelled tool results..."}
	case RecoveryErrorTypeThinkingBlockOrder:
		return RecoveryToastContent{"Thinking Block Recovery", "Fixing message structure..."}
	case RecoveryErrorTypeThinkingDisabledViolation:
		return RecoveryToastContent{"Thinking Strip Recovery", "Stripping thinking blocks..."}
	default:
		return RecoveryToastContent{"Session Recovery", "Attempting to recover session..."}
	}
}

// GetRecoverySuccessToast returns the reference success toast content.
func GetRecoverySuccessToast() RecoveryToastContent {
	return RecoveryToastContent{"Session Recovered", "Continuing where you left off..."}
}

// GetRecoveryFailureToast returns the reference failure toast content.
func GetRecoveryFailureToast() RecoveryToastContent {
	return RecoveryToastContent{"Recovery Failed", "Please retry or start a new session."}
}

func writeJSON(path string, value any) bool {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return false
	}
	return os.WriteFile(path, b, 0o644) == nil
}
func pathExists(path string) bool { _, e := os.Stat(path); return e == nil }
func xdgDataHome() string {
	if runtime.GOOS == "windows" {
		if v := os.Getenv("APPDATA"); v != "" {
			return v
		}
		if h, e := os.UserHomeDir(); e == nil && h != "" {
			return filepath.Join(h, "AppData", "Roaming")
		}
		return ""
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	if h, e := os.UserHomeDir(); e == nil && h != "" {
		return filepath.Join(h, ".local", "share")
	}
	return ""
}

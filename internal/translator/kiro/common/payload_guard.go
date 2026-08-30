package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

const (
	defaultKiroMaxPayloadTokens = 800_000
	defaultKiroMaxPayloadBytes  = 1_085_435
)

var (
	kiroPayloadTokenizerOnce sync.Once
	kiroPayloadTokenizer     tokenizer.Codec
	kiroPayloadTokenizerErr  error
)

type KiroPayloadGuardStats struct {
	OriginalBytes          int
	FinalBytes             int
	OriginalTokens         int
	FinalTokens            int
	OriginalHistoryEntries int
	FinalHistoryEntries    int
	Trimmed                bool
}

type KiroPayloadTooLargeError struct {
	ModelID    string
	Bytes      int
	ByteLimit  int
	Tokens     int
	TokenLimit int
}

func (e *KiroPayloadTooLargeError) Error() string {
	return fmt.Sprintf(
		"kiro payload too large for %s: %d tokens (limit %d), %d bytes (limit %d); shorten the conversation or send fewer tools, or set AUTO_TRIM_PAYLOAD=true",
		e.ModelID,
		e.Tokens,
		e.TokenLimit,
		e.Bytes,
		e.ByteLimit,
	)
}

func GuardKiroPayload(payload []byte, modelID string) ([]byte, KiroPayloadGuardStats, error) {
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, KiroPayloadGuardStats{}, fmt.Errorf("decode Kiro payload for size guard: %w", err)
	}

	encoded, err := marshalCompactKiroPayload(decoded)
	if err != nil {
		return nil, KiroPayloadGuardStats{}, err
	}
	tokenLimit := kiroPayloadLimitFromEnv("KIRO_MAX_PAYLOAD_TOKENS", defaultKiroMaxPayloadTokens)
	byteLimit := kiroPayloadLimitFromEnv("KIRO_MAX_PAYLOAD_BYTES", defaultKiroMaxPayloadBytes)
	originalTokens, err := countKiroPayloadTokens(encoded)
	if err != nil {
		return nil, KiroPayloadGuardStats{}, err
	}
	history := kiroPayloadHistory(decoded)
	stats := KiroPayloadGuardStats{
		OriginalBytes:          len(encoded),
		FinalBytes:             len(encoded),
		OriginalTokens:         originalTokens,
		FinalTokens:            originalTokens,
		OriginalHistoryEntries: len(history),
		FinalHistoryEntries:    len(history),
	}
	if !kiroPayloadOverLimit(stats.FinalBytes, stats.FinalTokens, byteLimit, tokenLimit) {
		return encoded, stats, nil
	}
	if !kiroAutoTrimPayload() || len(history) == 0 {
		return nil, stats, newKiroPayloadTooLargeError(modelID, stats, byteLimit, tokenLimit)
	}

	stripEmptyKiroToolUses(history)
	for len(history) > 2 {
		encoded, err = marshalCompactKiroPayload(decoded)
		if err != nil {
			return nil, stats, err
		}
		currentTokens, countErr := countKiroPayloadTokens(encoded)
		if countErr != nil {
			return nil, stats, countErr
		}
		if !kiroPayloadOverLimit(len(encoded), currentTokens, byteLimit, tokenLimit) {
			break
		}
		history = history[2:]
		setKiroPayloadHistory(decoded, history)
	}

	history = alignKiroHistoryToUser(history)
	setKiroPayloadHistory(decoded, history)
	repairTrimmedKiroToolResults(history)

	encoded, err = marshalCompactKiroPayload(decoded)
	if err != nil {
		return nil, stats, err
	}
	finalTokens, err := countKiroPayloadTokens(encoded)
	if err != nil {
		return nil, stats, err
	}
	stats.FinalBytes = len(encoded)
	stats.FinalTokens = finalTokens
	stats.FinalHistoryEntries = len(history)
	stats.Trimmed = stats.FinalHistoryEntries != stats.OriginalHistoryEntries
	if kiroPayloadOverLimit(stats.FinalBytes, stats.FinalTokens, byteLimit, tokenLimit) {
		return nil, stats, newKiroPayloadTooLargeError(modelID, stats, byteLimit, tokenLimit)
	}
	return encoded, stats, nil
}

func newKiroPayloadTooLargeError(modelID string, stats KiroPayloadGuardStats, byteLimit, tokenLimit int) error {
	return &KiroPayloadTooLargeError{
		ModelID:    modelID,
		Bytes:      stats.FinalBytes,
		ByteLimit:  byteLimit,
		Tokens:     stats.FinalTokens,
		TokenLimit: tokenLimit,
	}
}

func marshalCompactKiroPayload(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode compact Kiro payload: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

func countKiroPayloadTokens(payload []byte) (int, error) {
	kiroPayloadTokenizerOnce.Do(func() {
		kiroPayloadTokenizer, kiroPayloadTokenizerErr = tokenizer.Get(tokenizer.Cl100kBase)
	})
	if kiroPayloadTokenizerErr != nil {
		return 0, fmt.Errorf("initialize Kiro cl100k tokenizer: %w", kiroPayloadTokenizerErr)
	}
	count, err := kiroPayloadTokenizer.Count(string(payload))
	if err != nil {
		return 0, fmt.Errorf("count Kiro payload tokens: %w", err)
	}
	return count, nil
}

func kiroPayloadLimitFromEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func kiroAutoTrimPayload() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTO_TRIM_PAYLOAD"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func kiroPayloadOverLimit(bytesCount, tokenCount, byteLimit, tokenLimit int) bool {
	return (tokenLimit > 0 && tokenCount > tokenLimit) || (byteLimit > 0 && bytesCount > byteLimit)
}

func kiroPayloadHistory(payload map[string]any) []any {
	conversation, _ := payload["conversationState"].(map[string]any)
	history, _ := conversation["history"].([]any)
	return history
}

func setKiroPayloadHistory(payload map[string]any, history []any) {
	conversation, _ := payload["conversationState"].(map[string]any)
	if len(history) == 0 {
		delete(conversation, "history")
		return
	}
	conversation["history"] = history
}

func stripEmptyKiroToolUses(history []any) {
	for _, entryValue := range history {
		entry, _ := entryValue.(map[string]any)
		assistant, _ := entry["assistantResponseMessage"].(map[string]any)
		toolUses, exists := assistant["toolUses"].([]any)
		if exists && len(toolUses) == 0 {
			delete(assistant, "toolUses")
		}
	}
}

func alignKiroHistoryToUser(history []any) []any {
	for len(history) > 0 {
		entry, _ := history[0].(map[string]any)
		if _, ok := entry["userInputMessage"]; ok {
			break
		}
		history = history[1:]
	}
	return history
}

func repairTrimmedKiroToolResults(history []any) {
	for index, entryValue := range history {
		entry, _ := entryValue.(map[string]any)
		user, _ := entry["userInputMessage"].(map[string]any)
		if user == nil {
			continue
		}
		contextValue, _ := user["userInputMessageContext"].(map[string]any)
		toolResults, ok := contextValue["toolResults"].([]any)
		if !ok {
			continue
		}

		validIDs := make(map[string]struct{})
		if index > 0 {
			previous, _ := history[index-1].(map[string]any)
			assistant, _ := previous["assistantResponseMessage"].(map[string]any)
			toolUses, _ := assistant["toolUses"].([]any)
			for _, toolUseValue := range toolUses {
				toolUse, _ := toolUseValue.(map[string]any)
				if id, _ := toolUse["toolUseId"].(string); id != "" {
					validIDs[id] = struct{}{}
				}
			}
		}

		kept := make([]any, 0, len(toolResults))
		orphanedText := make([]string, 0)
		for _, resultValue := range toolResults {
			result, _ := resultValue.(map[string]any)
			id, _ := result["toolUseId"].(string)
			if _, valid := validIDs[id]; valid {
				kept = append(kept, resultValue)
				continue
			}
			orphanedText = append(orphanedText, kiroToolResultText(result["content"])...)
		}
		if len(kept) == len(toolResults) {
			continue
		}
		if len(kept) > 0 {
			contextValue["toolResults"] = kept
		} else {
			delete(contextValue, "toolResults")
			if len(contextValue) == 0 {
				delete(user, "userInputMessageContext")
			}
		}
		if len(orphanedText) > 0 {
			content, _ := user["content"].(string)
			user["content"] = content + "\n[trimmed tool result] " + strings.Join(orphanedText, "; ")
		}
	}
}

func kiroToolResultText(content any) []string {
	switch typed := content.(type) {
	case string:
		if typed != "" {
			return []string{typed}
		}
	case []any:
		texts := make([]string, 0, len(typed))
		for _, partValue := range typed {
			part, _ := partValue.(map[string]any)
			if text, _ := part["text"].(string); text != "" {
				texts = append(texts, text)
			}
		}
		return texts
	}
	return nil
}

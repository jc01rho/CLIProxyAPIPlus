package openai

import (
	"bytes"
	"context"
)

// ConvertCursorResponseToOpenAI normalizes a Cursor streaming chunk that is
// already OpenAI Chat Completions SSE. The executor emits OpenAI-shaped JSON.
func ConvertCursorResponseToOpenAI(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) [][]byte {
	rawJSON = bytes.TrimSpace(rawJSON)
	if len(rawJSON) == 0 {
		return nil
	}
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return nil
	}
	return [][]byte{rawJSON}
}

// ConvertCursorResponseToOpenAINonStream passes through a non-streaming Cursor
// response that is already OpenAI Chat Completions JSON.
func ConvertCursorResponseToOpenAINonStream(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) []byte {
	return rawJSON
}

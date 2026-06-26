package executor

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestNormalizeDeltaContentArray(t *testing.T) {
	t.Run("no data prefix unchanged", func(t *testing.T) {
		input := `{"choices":[{"delta":{"content":"hello"}}]}`
		got := normalizeDeltaContentArray([]byte(input))
		if string(got) != input {
			t.Errorf("got %q, want %q", got, input)
		}
	})

	t.Run("data DONE unchanged", func(t *testing.T) {
		input := "data: [DONE]"
		got := normalizeDeltaContentArray([]byte(input))
		if string(got) != input {
			t.Errorf("got %q, want %q", got, input)
		}
	})

	t.Run("empty choices unchanged", func(t *testing.T) {
		input := `data: {"choices":[]}`
		got := normalizeDeltaContentArray([]byte(input))
		if string(got) != input {
			t.Errorf("got %q, want %q", got, input)
		}
	})

	t.Run("string content unchanged", func(t *testing.T) {
		input := `data: {"choices":[{"delta":{"content":"hello"}}]}`
		got := normalizeDeltaContentArray([]byte(input))
		if string(got) != input {
			t.Errorf("got %q, want %q", got, input)
		}
	})

	t.Run("array content normalized strips thinking", func(t *testing.T) {
		input := `data: {"choices":[{"delta":{"content":[{"type":"text","text":"hello"},{"type":"thinking","text":"ignore"}]}}]}`
		got := normalizeDeltaContentArray([]byte(input))
		jsonPart := got[len("data: "):]
		var obj struct {
			Choices []struct {
				Delta struct {
					Content json.RawMessage `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(jsonPart, &obj); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		var s string
		if err := json.Unmarshal(obj.Choices[0].Delta.Content, &s); err != nil {
			t.Fatalf("content not string: %v", err)
		}
		if s != "hello" {
			t.Errorf("content = %q, want %q", s, "hello")
		}
	})

	t.Run("multiple choices array content all normalized", func(t *testing.T) {
		input := `data: {"choices":[{"delta":{"content":[{"type":"text","text":"a"},{"type":"thinking","text":"skip"}]}},{"delta":{"content":[{"type":"text","text":"b"}]}}]}`
		got := normalizeDeltaContentArray([]byte(input))
		jsonPart := got[len("data: "):]
		var obj struct {
			Choices []struct {
				Delta struct {
					Content json.RawMessage `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(jsonPart, &obj); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		wantContents := []string{"a", "b"}
		if len(obj.Choices) != len(wantContents) {
			t.Fatalf("got %d choices, want %d", len(obj.Choices), len(wantContents))
		}
		for i, want := range wantContents {
			var s string
			if err := json.Unmarshal(obj.Choices[i].Delta.Content, &s); err != nil {
				t.Fatalf("choice %d content not string: %v", i, err)
			}
			if s != want {
				t.Errorf("choice %d content = %q, want %q", i, s, want)
			}
		}
	})

	t.Run("SSE line without choices unchanged", func(t *testing.T) {
		input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk"}`
		got := normalizeDeltaContentArray([]byte(input))
		if string(got) != input {
			t.Errorf("got %q, want %q", got, input)
		}
	})

	t.Run("multiple text parts in one choice joined", func(t *testing.T) {
		input := `data: {"choices":[{"delta":{"content":[{"type":"text","text":"hello"},{"type":"text","text":" world"}]}}]}`
		got := normalizeDeltaContentArray([]byte(input))
		jsonPart := got[len("data: "):]
		var obj struct {
			Choices []struct {
				Delta struct {
					Content json.RawMessage `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(jsonPart, &obj); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		var s string
		if err := json.Unmarshal(obj.Choices[0].Delta.Content, &s); err != nil {
			t.Fatalf("content not string: %v", err)
		}
		if s != "hello world" {
			t.Errorf("content = %q, want %q", s, "hello world")
		}
	})
}

func TestStripOpenAICompatProviderUnsupportedFields_Kimi(t *testing.T) {
	payload := []byte(`{"model":"kimi-k2.5","messages":[],"reasoning_effort":"high","reasoning":{"enabled":true},"reasoningSummary":"auto","include":["reasoning"],"verbosity":"detailed"}`)
	compat := &config.OpenAICompatibility{Name: "kimi", BaseURL: "https://api.moonshot.cn/v1"}

	got := stripOpenAICompatProviderUnsupportedFields("openai-compatible-kimi", compat, payload)
	for _, field := range []string{"reasoning_effort", "reasoning", "reasoningSummary", "include", "verbosity"} {
		if gjson.GetBytes(got, field).Exists() {
			t.Fatalf("%s should be removed for OpenAI-compatible Kimi provider, got %s", field, got)
		}
	}
	if model := gjson.GetBytes(got, "model").String(); model != "kimi-k2.5" {
		t.Fatalf("model = %q, want kimi-k2.5; payload=%s", model, got)
	}
}

func TestStripOpenAICompatProviderUnsupportedFields_NonKimiUnchanged(t *testing.T) {
	payload := []byte(`{"model":"glm-5","messages":[],"reasoning_effort":"max"}`)
	compat := &config.OpenAICompatibility{Name: "glm", BaseURL: "https://glm.example/v1"}

	got := stripOpenAICompatProviderUnsupportedFields("openai-compatible-glm", compat, payload)
	if string(got) != string(payload) {
		t.Fatalf("non-Kimi compat payload changed: got %s want %s", got, payload)
	}
}

func TestFixMistralMessageOrder(t *testing.T) {
	e := &OpenAICompatExecutor{}

	t.Run("empty messages", func(t *testing.T) {
		input := []byte(`{"messages":[]}`)
		got := e.fixMistralMessageOrder(input)
		if string(got) != string(input) {
			t.Errorf("got %s, want unchanged", got)
		}
	})

	t.Run("last message is user", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
		got := e.fixMistralMessageOrder(input)
		if string(got) != string(input) {
			t.Errorf("got %s, want unchanged", got)
		}
	})

	t.Run("last assistant no prefix field adds prefix true", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`)
		got := e.fixMistralMessageOrder(input)
		var obj struct {
			Messages []struct {
				Role   string `json:"role"`
				Prefix *bool  `json:"prefix,omitempty"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(got, &obj); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		last := obj.Messages[len(obj.Messages)-1]
		if last.Role != "assistant" {
			t.Fatalf("last role = %q, want assistant", last.Role)
		}
		if last.Prefix == nil || !*last.Prefix {
			t.Errorf("expected prefix=true, got %v", last.Prefix)
		}
	})

	t.Run("last assistant with prefix true unchanged", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","prefix":true}]}`)
		got := e.fixMistralMessageOrder(input)
		var obj struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(got, &obj); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(obj.Messages) != 2 {
			t.Errorf("got %d messages, want 2", len(obj.Messages))
		}
	})

	t.Run("last assistant with prefix false appends placeholder user", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","prefix":false}]}`)
		got := e.fixMistralMessageOrder(input)
		var obj struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(got, &obj); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(obj.Messages) != 3 {
			t.Fatalf("got %d messages, want 3", len(obj.Messages))
		}
		last := obj.Messages[len(obj.Messages)-1]
		if last.Role != "user" {
			t.Errorf("appended role = %q, want user", last.Role)
		}
		if last.Content != "." {
			t.Errorf("appended content = %q, want '.'", last.Content)
		}
	})

	t.Run("last message is tool unchanged", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"result","tool_call_id":"call_1"}]}`)
		got := e.fixMistralMessageOrder(input)
		if string(got) != string(input) {
			t.Errorf("got %s, want unchanged", got)
		}
	})

	t.Run("no messages field unchanged", func(t *testing.T) {
		input := []byte(`{"model":"mistral-large","temperature":0.7}`)
		got := e.fixMistralMessageOrder(input)
		if string(got) != string(input) {
			t.Errorf("got %s, want unchanged", got)
		}
	})
}

package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestSplitMiniMaxThinking(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		wantReasoning string
		wantCleaned   string
	}{
		{
			name:          "no tags passes through",
			content:       "plain answer",
			wantReasoning: "",
			wantCleaned:   "plain answer",
		},
		{
			name:          "single thinking block",
			content:       "<thinking>let me think</response>Here is the answer",
			wantReasoning: "let me think",
			wantCleaned:   "Here is the answer",
		},
		{
			name:          "closing tag is response",
			content:       "<thinking>the user says hi</response>\nThe answer is hi",
			wantReasoning: "the user says hi",
			wantCleaned:   "\nThe answer is hi",
		},
		{
			name:          "unterminated thinking",
			content:       "<thinking>cut off reasoning",
			wantReasoning: "cut off reasoning",
			wantCleaned:   "",
		},
		{
			name:          "leading text before thinking",
			content:       "intro <thinking>think here</response>answer",
			wantReasoning: "think here",
			wantCleaned:   "intro answer",
		},
		{
			name:          "interleaved multiple blocks",
			content:       "<thinking>one</response>mid<thinking>two</response>end",
			wantReasoning: "onetwo",
			wantCleaned:   "midend",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReasoning, gotCleaned := splitMiniMaxThinking(tt.content)
			if gotReasoning != tt.wantReasoning {
				t.Fatalf("reasoning = %q, want %q", gotReasoning, tt.wantReasoning)
			}
			if gotCleaned != tt.wantCleaned {
				t.Fatalf("cleaned = %q, want %q", gotCleaned, tt.wantCleaned)
			}
		})
	}
}

func TestNormalizeMiniMaxThinkingBody(t *testing.T) {
	body := []byte(`{"model":"MiniMax-M3","choices":[{"index":0,"message":{"role":"assistant","content":"<thinking>say hi</response>The greeting is hi"}}]}`)
	out := normalizeMiniMaxThinkingBody(body)
	content := gjson.GetBytes(out, "choices.0.message.content").String()
	reasoning := gjson.GetBytes(out, "choices.0.message.reasoning_content").String()
	if content != "The greeting is hi" {
		t.Fatalf("content = %q, want %q", content, "The greeting is hi")
	}
	if reasoning != "say hi" {
		t.Fatalf("reasoning_content = %q, want %q", reasoning, "say hi")
	}
}

func TestNormalizeMiniMaxThinkingBodySeparatesThinkTags(t *testing.T) {
	body := []byte(`{"id":"06e00be8e4c2cdc864f5e1c3e351f8de","choices":[{"finish_reason":"stop","index":0,"message":{"content":"<think>The user says hello.</think>\n\nHi there!","role":"assistant"}}],"model":"MiniMax-M3","object":"chat.completion"}`)

	out := normalizeMiniMaxThinkingBody(body)

	if got := gjson.GetBytes(out, "choices.0.message.reasoning_content").String(); got != "The user says hello." {
		t.Fatalf("reasoning_content = %q, want separated think text; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "\n\nHi there!" {
		t.Fatalf("content = %q, want answer without think block; body=%s", got, out)
	}
}

func TestOpenAICompatExecutorSeparatesMiniMaxM3ThinkTags(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"06e00be8e4c2cdc864f5e1c3e351f8de","choices":[{"finish_reason":"stop","index":0,"message":{"content":"<think>The user says hello.</think>\n\nHi there!","role":"assistant"}}],"model":"MiniMax-M3","object":"chat.completion"}`))
	}))
	defer upstream.Close()

	openAICompat := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url": upstream.URL,
			"api_key":  "test-key",
		},
	}
	request := cliproxyexecutor.Request{
		Model:   "MiniMax-M3",
		Payload: []byte(`{"model":"MiniMax-M3","messages":[{"role":"user","content":"Hello"}]}`),
	}
	options := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}

	response, err := openAICompat.Execute(context.Background(), auth, request, options)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.reasoning_content").String(); got != "The user says hello." {
		t.Fatalf("reasoning_content = %q, want separated think text; body=%s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != "\n\nHi there!" {
		t.Fatalf("content = %q, want answer without think block; body=%s", got, response.Payload)
	}
}

func TestNormalizeMiniMaxThinkingBodyNoTags(t *testing.T) {
	body := []byte(`{"model":"MiniMax-M3","choices":[{"index":0,"message":{"role":"assistant","content":"plain"}}]}`)
	out := normalizeMiniMaxThinkingBody(body)
	if gjson.GetBytes(out, "choices.0.message.reasoning_content").Exists() {
		t.Fatalf("should not add reasoning_content for plain content: %s", out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "plain" {
		t.Fatalf("content = %q, want plain", got)
	}
}

func TestMiniMaxThinkingStreamState(t *testing.T) {
	st := &minimaxThinkingStreamState{}

	// Frame 1: opening tag + partial reasoning.
	r, c := st.feed("<thinking>say ")
	if r != "" || c != "" {
		t.Fatalf("frame1 reasoning=%q content=%q, want empty", r, c)
	}

	// Frame 2: rest of reasoning.
	r, c = st.feed("hi</response>done")
	if r != "say hi" {
		t.Fatalf("frame2 reasoning=%q, want %q", r, "say hi")
	}
	if c != "done" {
		t.Fatalf("frame2 content=%q, want done", c)
	}

	// Subsequent plain frame emits as content.
	r, c = st.feed(" next")
	if r != "" || c != " next" {
		t.Fatalf("frame3 reasoning=%q content=%q, want '' ' next'", r, c)
	}
}

func TestMiniMaxThinkingStreamStatePartialCloseAcrossFrames(t *testing.T) {
	st := &minimaxThinkingStreamState{}
	// Opening tag closes across frame boundary.
	r, c := st.feed("<thinking>reason")
	if r != "" || c != "" {
		t.Fatalf("f1 r=%q c=%q", r, c)
	}
	r, c = st.feed("ing</resp")
	if r != "" || c != "" {
		t.Fatalf("f2 (partial close) r=%q c=%q", r, c)
	}
	r, c = st.feed("onse>answer")
	if r != "reasoning" {
		t.Fatalf("f3 r=%q, want reasoning", r)
	}
	if c != "answer" {
		t.Fatalf("f3 c=%q, want answer", c)
	}
}

func TestNormalizeMiniMaxThinkingStream(t *testing.T) {
	st := &minimaxThinkingStreamState{}
	frame1 := normalizeMiniMaxThinkingStream(st, []byte(`{"choices":[{"index":0,"delta":{"content":"<thinking>think"}}]}`))
	frame2 := normalizeMiniMaxThinkingStream(st, []byte(`{"choices":[{"index":0,"delta":{"content":"ing</response>answer"}}]}`))

	if c := gjson.GetBytes(frame1, "choices.0.delta.content").String(); c != "" {
		t.Fatalf("frame1 content = %q, want empty", c)
	}
	if rc := gjson.GetBytes(frame2, "choices.0.delta.reasoning_content").String(); rc != "thinking" {
		t.Fatalf("frame2 reasoning = %q, want thinking", rc)
	}
	if c := gjson.GetBytes(frame2, "choices.0.delta.content").String(); c != "answer" {
		t.Fatalf("frame2 content = %q, want answer", c)
	}
}

func TestNormalizeMiniMaxThinkingStreamSeparatesSplitThinkTags(t *testing.T) {
	st := &minimaxThinkingStreamState{}
	frame1 := normalizeMiniMaxThinkingStream(st, []byte(`{"choices":[{"index":0,"delta":{"content":"<thi"}}]}`))
	frame2 := normalizeMiniMaxThinkingStream(st, []byte(`{"choices":[{"index":0,"delta":{"content":"nk>The user says hello.</thi"}}]}`))
	frame3 := normalizeMiniMaxThinkingStream(st, []byte(`{"choices":[{"index":0,"delta":{"content":"nk>\n\nHi there!"}}]}`))

	if got := gjson.GetBytes(frame1, "choices.0.delta.content").String(); got != "" {
		t.Fatalf("frame1 content = %q, want buffered partial opening tag", got)
	}
	if got := gjson.GetBytes(frame2, "choices.0.delta.content").String(); got != "" {
		t.Fatalf("frame2 content = %q, want buffered reasoning", got)
	}
	if got := gjson.GetBytes(frame3, "choices.0.delta.reasoning_content").String(); got != "The user says hello." {
		t.Fatalf("frame3 reasoning_content = %q, want separated think text; frame=%s", got, frame3)
	}
	if got := gjson.GetBytes(frame3, "choices.0.delta.content").String(); got != "\n\nHi there!" {
		t.Fatalf("frame3 content = %q, want answer without think block; frame=%s", got, frame3)
	}
}

func TestOpenAICompatExecutorStreamSeparatesSplitMiniMaxM3ThinkTags(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"MiniMax-M3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"<thi\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"MiniMax-M3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"nk>The user says hello.</thi\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"MiniMax-M3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"nk>\\n\\nHi there!\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	openAICompat := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url": upstream.URL,
			"api_key":  "test-key",
		},
	}
	request := cliproxyexecutor.Request{
		Model:   "MiniMax-M3",
		Payload: []byte(`{"model":"MiniMax-M3","stream":true,"messages":[{"role":"user","content":"Hello"}]}`),
	}
	options := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	}

	response, err := openAICompat.ExecuteStream(context.Background(), auth, request, options)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var output strings.Builder
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	got := output.String()
	if strings.Contains(got, "<think>") || strings.Contains(got, "</think>") {
		t.Fatalf("stream leaked think tags: %s", got)
	}
	if !strings.Contains(got, `"reasoning_content":"The user says hello."`) {
		t.Fatalf("stream missing separated reasoning_content: %s", got)
	}
	if !strings.Contains(got, `"content":"\n\nHi there!"`) {
		t.Fatalf("stream missing answer content: %s", got)
	}
}

func TestMiniMaxThinkingStreamFlush(t *testing.T) {
	st := &minimaxThinkingStreamState{}
	st.feed("<thinking>unfinished reasoning")
	reasoning, content := st.flush()
	if reasoning != "unfinished reasoning" || content != "" {
		t.Fatalf("flush reasoning=%q content=%q, want unfinished reasoning", reasoning, content)
	}
	// flush is idempotent — second call returns empty.
	if againReasoning, againContent := st.flush(); againReasoning != "" || againContent != "" {
		t.Fatalf("second flush reasoning=%q content=%q, want empty", againReasoning, againContent)
	}
}

func TestBuildMiniMaxThinkingFlushFrame(t *testing.T) {
	base := []byte(`{"id":"chatcmpl-1","model":"MiniMax-M3","choices":[{"index":0,"delta":{"content":"x"}}]}`)
	frame := buildMiniMaxThinkingFlushFrame(base, "leftover thinking", "")
	if got := gjson.GetBytes(frame, "choices.0.delta.reasoning_content").String(); got != "leftover thinking" {
		t.Fatalf("reasoning = %q", got)
	}
	if got := gjson.GetBytes(frame, "model").String(); got != "MiniMax-M3" {
		t.Fatalf("model = %q, want MiniMax-M3", got)
	}
	if got := gjson.GetBytes(frame, "id").String(); got != "chatcmpl-1" {
		t.Fatalf("id = %q, want chatcmpl-1", got)
	}
}

func TestIsMiniMaxThinkingTagModel(t *testing.T) {
	for _, model := range []string{"by-minimax", "MiniMax-M3", "minimax-m3", "  MiniMax-M3  "} {
		if !isMiniMaxThinkingTagModel(model) {
			t.Fatalf("expected %q to match", model)
		}
	}
	for _, model := range []string{"gpt-4o", "claude-opus", "deepseek-v3"} {
		if isMiniMaxThinkingTagModel(model) {
			t.Fatalf("expected %q NOT to match", model)
		}
	}
}

package executor

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestIslandHashReversesMD5Hex(t *testing.T) {
	// reverse(md5("abc")) == "900150983cd24fb0d6963f7d28e17f72" reversed.
	const want = "27f71e82d7f3696d0bf42dc389051009"
	if got := islandHash("abc"); got != want {
		t.Fatalf("islandHash(abc) = %q, want %q", got, want)
	}
}

func TestMintDeepAITryItKeyShape(t *testing.T) {
	key := mintDeepAITryItKey(deepAIUserAgent)
	parts := strings.Split(key, "-")
	if len(parts) != 3 || parts[0] != "tryit" {
		t.Fatalf("unexpected tryit key shape %q", key)
	}
	if len(parts[1]) == 0 || len(parts[2]) != 32 {
		t.Fatalf("unexpected tryit key digits/hash %q", key)
	}
	if key == mintDeepAITryItKey(deepAIUserAgent) {
		t.Fatal("expected a fresh single-use key per call")
	}
}

func TestDeepAIBuildFormMatchesBrowserDialect(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"text","text":"again"}]}]}`)
	body, contentType, history, err := deepAIBuildForm(payload, "super_genius", true)
	if err != nil {
		t.Fatalf("build form: %v", err)
	}
	if len(history) != 4 || history[0].Role != "system" || history[3].Content != "again" {
		t.Fatalf("unexpected history %+v", history)
	}

	mediaType, params, errMedia := mime.ParseMediaType(contentType)
	if errMedia != nil || mediaType != "multipart/form-data" {
		t.Fatalf("unexpected content type %q: %v", contentType, errMedia)
	}
	reader := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
	fields := map[string]string{}
	for {
		part, errPart := reader.NextPart()
		if errPart == io.EOF {
			break
		}
		if errPart != nil {
			t.Fatalf("read part: %v", errPart)
		}
		value, _ := io.ReadAll(part)
		fields[part.FormName()] = string(value)
	}

	want := map[string]string{
		"chat_style":                  "chat",
		"model":                       "super_genius",
		"hacker_is_stinky":            "very_stinky",
		"tool_activity_support":       "1",
		"thinking_image_tool_support": "1",
		"enabled_tools":               `["image_generator","image_editor"]`,
		"web_access_enabled":          "true",
	}
	for key, expected := range want {
		if fields[key] != expected {
			t.Fatalf("field %s = %q, want %q", key, fields[key], expected)
		}
	}
	if !strings.Contains(fields["chatHistory"], `"role":"user","content":"hello"`) {
		t.Fatalf("chatHistory missing history: %s", fields["chatHistory"])
	}
	if _, errUUID := json.Marshal(fields["session_uuid"]); errUUID != nil {
		t.Fatalf("session uuid must be a plain string: %v", errUUID)
	}
	if len(fields["session_uuid"]) != 36 {
		t.Fatalf("expected a UUID-shaped session id, got %q", fields["session_uuid"])
	}
}

func TestDeepAIBuildFormRejectsEmptyHistory(t *testing.T) {
	_, _, _, err := deepAIBuildForm([]byte(`{"messages":[{"role":"user","content":"  "}]}`), "standard", false)
	if err == nil {
		t.Fatal("expected an error for an empty chat history")
	}
}

func TestParseDeepAIStreamSplitsPackets(t *testing.T) {
	raw := "\x1c{\"tool_activity\":\"Searching the web...\"}\x1c" +
		"\x1dTHINKING_START3s\x1eI should answer briefly\x1dTHINKING_END" +
		"The sky is blue." +
		"\x1c{\"type\":\"generated_image\",\"share_url\":\"https://img.example/x.png\"}"

	got := parseDeepAIStream(raw)
	if got.text != "The sky is blue." {
		t.Fatalf("unexpected text %q", got.text)
	}
	if strings.ContainsAny(got.text, "\x1c\x1d\x1e") {
		t.Fatalf("control characters leaked into prose: %q", got.text)
	}
	if got.thinking != "I should answer briefly" {
		t.Fatalf("unexpected thinking %q", got.thinking)
	}
	if len(got.images) != 1 || got.images[0] != "https://img.example/x.png" {
		t.Fatalf("unexpected images %+v", got.images)
	}
}

func TestParseDeepAIStreamWithholdsPartialThinkingMarker(t *testing.T) {
	partial := "visible text\x1dTHINK"
	got := parseDeepAIStream(partial)
	if strings.Contains(got.text, "\x1d") || strings.HasSuffix(got.text, "THINK") {
		t.Fatalf("partial marker leaked into prose: %q", got.text)
	}
	if !strings.HasPrefix("visible text", got.text) {
		t.Fatalf("expected the stable prose prefix, got %q", got.text)
	}
}

func TestDeepAIModelDefault(t *testing.T) {
	cases := map[string]string{
		"deepai-standard":     "standard",
		"deepai-genius":       "genius",
		"deepai-super-genius": "super_genius",
		"gpt-4o-mini":         "gpt-4o-mini",
		"mini":                "mini",
	}
	for input, want := range cases {
		if got := deepAIModelDefault(input); got != want {
			t.Fatalf("deepAIModelDefault(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveDeepAIModelNameUsesConfiguredAlias(t *testing.T) {
	cfg := &config.Config{DeepAIKey: []config.DeepAIKey{{
		APIKey: "dashboard-key",
		Models: []config.DeepAIModel{{Name: "super_genius", Alias: "smart"}},
	}}}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "dashboard-key"}}
	if got := resolveDeepAIModelName(cfg, auth, "smart"); got != "super_genius" {
		t.Fatalf("alias resolved to %q", got)
	}
	if got := resolveDeepAIModelName(cfg, nil, "deepai-genius"); got != "genius" {
		t.Fatalf("default resolved to %q", got)
	}
}

func TestDeepAIExecuteNonStreamAgainstStub(t *testing.T) {
	var gotKey, gotUserAgent, gotOrigin string
	var gotHistory string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != deepAIChatPath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotKey = r.Header.Get("api-key")
		gotUserAgent = r.Header.Get("User-Agent")
		gotOrigin = r.Header.Get("Origin")
		if errParse := r.ParseMultipartForm(4 << 20); errParse != nil {
			t.Fatalf("parse multipart: %v", errParse)
		}
		gotHistory = r.FormValue("chatHistory")
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		_, _ = w.Write([]byte("stub answer"))
	}))
	defer server.Close()

	exec := NewDeepAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "deepai-anon",
		Provider:   "deepai",
		Attributes: map[string]string{"api_key": "anonymous", "base_url": server.URL},
	}
	body := []byte(`{"model":"deepai-standard","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepai-standard",
		Payload: body,
		Format:  sdktranslator.FromString("openai"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: body})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !strings.HasPrefix(gotKey, "tryit-") {
		t.Fatalf("expected an anonymous tryit key, got %q", gotKey)
	}
	if gotUserAgent != deepAIUserAgent {
		t.Fatalf("unexpected user agent %q", gotUserAgent)
	}
	if gotOrigin != deepAIOrigin {
		t.Fatalf("unexpected origin %q", gotOrigin)
	}
	if !strings.Contains(gotHistory, `"content":"hi"`) {
		t.Fatalf("history not forwarded: %s", gotHistory)
	}

	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal(resp.Payload, &decoded); errUnmarshal != nil {
		t.Fatalf("decode response: %v (%s)", errUnmarshal, resp.Payload)
	}
	if len(decoded.Choices) == 0 || decoded.Choices[0].Message.Content != "stub answer" {
		t.Fatalf("unexpected payload %s", resp.Payload)
	}
}

func TestDeepAIExecuteStreamEmitsSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		_, _ = w.Write([]byte("hello "))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("world"))
	}))
	defer server.Close()

	exec := NewDeepAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "deepai-anon",
		Provider:   "deepai",
		Attributes: map[string]string{"api_key": "anonymous", "base_url": server.URL},
	}
	body := []byte(`{"model":"deepai-standard","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepai-standard",
		Payload: body,
		Format:  sdktranslator.FromString("openai"),
	}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: body})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}

	var builder strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		builder.Write(chunk.Payload)
	}
	streamed := builder.String()
	if !strings.Contains(streamed, "hello") || !strings.Contains(streamed, "world") {
		t.Fatalf("stream missing content: %s", streamed)
	}
	// The [DONE] sentinel belongs to the HTTP handler, not the executor; the
	// executor's contract is the translated chunk stream ending on a stop frame.
	if !strings.Contains(streamed, `"finish_reason":"stop"`) {
		t.Fatalf("stream missing stop frame: %s", streamed)
	}
}

func TestDeepAITaskPollingCompletes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case deepAIChatPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task_id":"task-1"}`))
		case deepAITaskStatusPath:
			if got := r.URL.Query().Get("type"); got != "thinking-task" {
				t.Errorf("unexpected task type %q", got)
			}
			_, _ = w.Write([]byte(`{"status":"COMPLETED","result":"final answer"}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	exec := NewDeepAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "deepai-anon",
		Provider:   "deepai",
		Attributes: map[string]string{"api_key": "anonymous", "base_url": server.URL},
	}
	body := []byte(`{"model":"deepai-standard","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepai-standard",
		Payload: body,
		Format:  sdktranslator.FromString("openai"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: body})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(string(resp.Payload), "final answer") {
		t.Fatalf("task result not surfaced: %s", resp.Payload)
	}
}

func TestDeepAIUpstreamErrorSurfacesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`Only paid accounts can use genius`))
	}))
	defer server.Close()

	exec := NewDeepAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "deepai-anon",
		Provider:   "deepai",
		Attributes: map[string]string{"api_key": "anonymous", "base_url": server.URL},
	}
	body := []byte(`{"model":"deepai-genius","messages":[{"role":"user","content":"hi"}]}`)
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepai-genius",
		Payload: body,
		Format:  sdktranslator.FromString("openai"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: body})
	if err == nil {
		t.Fatal("expected an upstream error")
	}
	if code := deepAIStatusCode(err); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%v)", code, err)
	}
	if !strings.Contains(err.Error(), "paid accounts") {
		t.Fatalf("error lost the upstream detail: %v", err)
	}
}

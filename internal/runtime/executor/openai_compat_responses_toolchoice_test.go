package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNativeResponsesForcedToolChoiceRetriesWithAuto(t *testing.T) {
	const forced = `{"input":"hi","tools":[{"type":"function","name":"todo","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"todo"}}`
	rejectBody := `{"error":{"type":"invalid_request_error","message":"bad","http_status":400,"retryable":false}}`
	failedEvent := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"invalid_request_error\",\"http_status\":400,\"retryable\":false}}}\n\n"
	okJSON := `{"id":"r","status":"completed","output":[{"type":"function_call","id":"fc","call_id":"c","name":"todo","arguments":"{}"}]}`
	okStream := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":" + okJSON + "}\n\n"

	for _, tc := range []struct {
		name        string
		payload     string
		stream      bool
		reject      string // "http" or "event"
		rejectCode  int
		wantChoices []string
		wantErr     bool
	}{
		{"non-stream http 400", forced, false, "http", 400, []string{"object", "auto"}, false},
		{"stream http 400", forced, true, "http", 400, []string{"object", "auto"}, false},
		{"stream early failed event", forced, true, "event", 400, []string{"object", "auto"}, false},
		{"required string", strings.Replace(forced, `{"type":"function","name":"todo"}`, `"required"`, 1), false, "http", 400, []string{"required", "auto"}, false},
		{"auto is not retried", strings.Replace(forced, `{"type":"function","name":"todo"}`, `"auto"`, 1), false, "http", 400, []string{"auto"}, true},
		{"503 is not retried", forced, false, "http", 503, []string{"object"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var choices []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				choice := gjson.GetBytes(raw, "tool_choice")
				label := choice.String()
				if choice.IsObject() {
					label = "object"
				}
				mu.Lock()
				choices = append(choices, label)
				first := len(choices) == 1
				mu.Unlock()
				if first && label != "auto" || tc.name == "auto is not retried" {
					if tc.reject == "event" {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, failedEvent)
						return
					}
					w.WriteHeader(tc.rejectCode)
					_, _ = io.WriteString(w, rejectBody)
					return
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, okStream)
					return
				}
				_, _ = io.WriteString(w, okJSON)
			}))
			defer server.Close()
			e, auth := nativeResponsesExecutor(server.URL)
			req := cliproxyexecutor.Request{Model: "upstream", Payload: []byte(tc.payload)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			var gotErr error
			var payload string
			if tc.stream {
				stream, err := e.ExecuteStream(context.Background(), auth, req, opts)
				gotErr = err
				if err == nil {
					var b strings.Builder
					for chunk := range stream.Chunks {
						b.Write(chunk.Payload)
						if chunk.Err != nil {
							gotErr = chunk.Err
						}
					}
					payload = b.String()
				}
			} else {
				resp, err := e.Execute(context.Background(), auth, req, opts)
				gotErr = err
				payload = string(resp.Payload)
			}
			if (gotErr != nil) != tc.wantErr {
				t.Fatalf("err=%v payload=%s", gotErr, payload)
			}
			if !tc.wantErr && payload == "" {
				t.Fatal("empty payload after retry")
			}
			if tc.stream && !tc.wantErr && strings.Count(payload, "event: response.created\n") != 1 {
				t.Fatalf("stream frames duplicated or missing: %s", payload)
			}
			if strings.Join(choices, ",") != strings.Join(tc.wantChoices, ",") {
				t.Fatalf("upstream tool_choice sequence = %v, want %v", choices, tc.wantChoices)
			}
		})
	}
}

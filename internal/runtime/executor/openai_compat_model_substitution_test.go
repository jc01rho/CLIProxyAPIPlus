package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestIsMaterialModelSubstitution(t *testing.T) {
	tests := []struct {
		requested, served string
		want              bool
	}{
		{"gpt-6-astra", "gpt-5.6-terra", true},
		{"gpt-6-astra", "gpt-6-astra", false},
		{"mimo-v2.5", "mimo-v2.5-free", false},
		{"mimo-v2.5:free", "mimo-v2.5", false},
		{"mimo-v2.5", "mimo-v2.5:free", false},
		{"mimo-v2.5-free", "mimo-v2.6-free", true},
		{"gpt-6-astra", "", false},
		{"gpt-6-astra", "gpt-6-astra-2026-10-01", false},
	}
	for _, tt := range tests {
		if got := helps.IsMaterialModelSubstitution(tt.requested, tt.served); got != tt.want {
			t.Errorf("IsMaterialModelSubstitution(%q, %q) = %v, want %v", tt.requested, tt.served, got, tt.want)
		}
	}
}

func substitutionResponse(model string) string {
	return `{"id":"r","status":"completed","model":"` + model + `","output":[{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
}

func substitutionStream(model string) string {
	return "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"" + model + "\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":" + substitutionResponse(model) + "}\n\n"
}

func runSubstitution(t *testing.T, reject bool, stream bool, models []string) (calls int32, payload string, err error) {
	t.Helper()
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		index := int(count.Add(1)) - 1
		if index >= len(models) {
			index = len(models) - 1
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, substitutionStream(models[index]))
			return
		}
		_, _ = io.WriteString(w, substitutionResponse(models[index]))
	}))
	defer server.Close()

	e, auth := nativeResponsesExecutor(server.URL)
	e.cfg.OpenAICompatibility[0].RejectModelSubstitution = reject
	req := cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"input":"hi"}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	if stream {
		result, errStream := e.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			return count.Load(), "", errStream
		}
		var b strings.Builder
		for chunk := range result.Chunks {
			b.Write(chunk.Payload)
			if chunk.Err != nil {
				err = chunk.Err
			}
		}
		return count.Load(), b.String(), err
	}
	resp, errExec := e.Execute(context.Background(), auth, req, opts)
	return count.Load(), string(resp.Payload), errExec
}

func TestNativeResponsesModelSubstitutionRetries(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "non-stream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			t.Run("recovers on the second try", func(t *testing.T) {
				calls, payload, err := runSubstitution(t, true, stream, []string{"other-model", "upstream"})
				if err != nil || calls != 2 || !strings.Contains(payload, "ok") {
					t.Fatalf("calls=%d err=%v payload=%s, want 2 calls and a delivered response", calls, err, payload)
				}
				if strings.Contains(payload, "other-model") {
					t.Fatalf("substituted response leaked to the client: %s", payload)
				}
			})
			t.Run("recovers on the last allowed retry", func(t *testing.T) {
				calls, _, err := runSubstitution(t, true, stream, []string{"other-model", "other-model", "upstream"})
				if err != nil || calls != 3 {
					t.Fatalf("calls=%d err=%v, want 3 calls (initial + 2 retries) and success", calls, err)
				}
			})
			t.Run("gives up after two retries", func(t *testing.T) {
				calls, payload, err := runSubstitution(t, true, stream, []string{"other-model"})
				if err == nil || calls != 3 {
					t.Fatalf("calls=%d err=%v payload=%s, want 3 calls then an error", calls, err, payload)
				}
				if strings.Contains(payload, "other-model") && !stream {
					t.Fatalf("substituted payload delivered on failure: %s", payload)
				}
			})
			t.Run("matching model is not retried", func(t *testing.T) {
				calls, _, err := runSubstitution(t, true, stream, []string{"upstream"})
				if err != nil || calls != 1 {
					t.Fatalf("calls=%d err=%v, want a single call", calls, err)
				}
			})
			t.Run("free-tier tag is not a substitution", func(t *testing.T) {
				calls, _, err := runSubstitution(t, true, stream, []string{"upstream-free"})
				if err != nil || calls != 1 {
					t.Fatalf("calls=%d err=%v, want a single call for a -free routing", calls, err)
				}
			})
			t.Run("option off keeps the response", func(t *testing.T) {
				calls, payload, err := runSubstitution(t, false, stream, []string{"other-model"})
				if err != nil || calls != 1 || !strings.Contains(payload, "ok") {
					t.Fatalf("calls=%d err=%v payload=%s, want the substituted response passed through once", calls, err, payload)
				}
			})
		})
	}
}

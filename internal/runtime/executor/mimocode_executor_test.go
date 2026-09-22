package executor

import (
	"context"
	"io"
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

func TestPrepareMimocodeAuthAppliesDefaultsAndPreservesOverride(t *testing.T) {
	prepared := prepareMimocodeAuth(&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "secret"}})
	if got := prepared.Attributes["base_url"]; got != mimocodeDefaultBaseURL {
		t.Fatalf("base_url = %q, want %q", got, mimocodeDefaultBaseURL)
	}
	if got := prepared.Attributes["header:X-Mimo-Source"]; got != mimocodeSourceValue {
		t.Fatalf("X-Mimo-Source = %q, want %q", got, mimocodeSourceValue)
	}

	overridden := prepareMimocodeAuth(&cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":             "https://region.example/v1",
		"header:x-mimo-source": "custom-client",
	}})
	if got := overridden.Attributes["base_url"]; got != "https://region.example/v1" {
		t.Fatalf("overridden base_url = %q", got)
	}
	if _, exists := overridden.Attributes["header:X-Mimo-Source"]; exists {
		t.Fatal("default source header was added despite user override")
	}
}

func TestMimocodeExecutorUsesOpenAIPathAndHeaders(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Mimo-Source"); got != "custom-client" {
			t.Errorf("X-Mimo-Source = %q, want custom-client", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewMimocodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Provider: "mimocode", Attributes: map[string]string{
		"api_key":              "secret",
		"base_url":             server.URL,
		"header:X-Mimo-Source": "custom-client",
	}}
	request := cliproxyexecutor.Request{
		Model:   "mimo-v2.5-pro",
		Payload: []byte(`{"model":"mimo-v2.5-pro","messages":[{"role":"assistant","content":"prior","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`),
	}
	_, errExecute := executor.Execute(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(receivedBody, "messages.0.reasoning_content").String(); got != "prior" {
		t.Fatalf("reasoning_content = %q, want prior; body=%s", got, receivedBody)
	}
}

func TestMimocodeExecutorRelabelsBlockedErrors(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{code: "421", want: "content moderation"},
		{code: "441", want: "risk control"},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":` + tc.code + `,"message":"blocked"}}`))
			}))
			defer server.Close()
			executor := NewMimocodeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "secret", "base_url": server.URL}}
			_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "mimo-v2.5", Payload: []byte(`{"model":"mimo-v2.5","messages":[]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
			if errExecute == nil || !strings.Contains(strings.ToLower(errExecute.Error()), tc.want) {
				t.Fatalf("Execute() error = %v, want %q", errExecute, tc.want)
			}
		})
	}
}

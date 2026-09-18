package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorSystemOnePassthrough(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-latest","answers":{"urgent":{"type":"noul","noul":0.99}},"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test-key",
	}}
	payload := []byte(`{"model":"jev-latest","state":"The checkout is broken.","questions":{"urgent":{"type":"noul","instructions":"Does this need immediate attention?"}}}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "jev-latest",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Alt:          "systemone",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/systemone" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/systemone")
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth = %q, want bearer test-key", gotAuth)
	}
	if string(gotBody) != string(payload) {
		t.Fatalf("body = %s, want verbatim payload", string(gotBody))
	}
	if gjson.GetBytes(resp.Payload, "answers.urgent.noul").Float() != 0.99 {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
	if gjson.GetBytes(resp.Payload, "messages").Exists() {
		t.Fatalf("unexpected chat translation in payload: %s", string(resp.Payload))
	}
}

func TestOpenAICompatSystemOneURL(t *testing.T) {
	cases := map[string]string{
		"https://api.typesafe.ai":     "https://api.typesafe.ai/v1/systemone",
		"https://api.typesafe.ai/":    "https://api.typesafe.ai/v1/systemone",
		"https://api.typesafe.ai/v1":  "https://api.typesafe.ai/v1/systemone",
		"https://api.typesafe.ai/v1/": "https://api.typesafe.ai/v1/systemone",
	}
	for base, want := range cases {
		if got := openAICompatSystemOneURL(base); got != want {
			t.Errorf("openAICompatSystemOneURL(%q) = %q, want %q", base, got, want)
		}
	}
}

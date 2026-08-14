package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestRejectInvalidCodexRequestFormatUniqueItemsInResponseSchema(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[],
		"text":{"format":{"type":"json_schema","name":"issue_exploration_plan","schema":{
			"type":"object",
			"properties":{
				"targets":{
					"type":"array",
					"items":{
						"type":"object",
						"properties":{
							"screen_terms":{"type":"array","uniqueItems":true}
						}
					}
				}
			}
		}}}
	}`)

	err := rejectInvalidCodexRequestFormat(body)
	if err == nil {
		t.Fatal("expected uniqueItems rejection")
	}
	var scoped cliproxyexecutor.RequestScopedError
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("error should be request-scoped: %T %v", err, err)
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %T %v, want 400", err, err)
	}
	payload := err.Error()
	if got := gjson.Get(payload, "error.code").String(); got != "invalid_json_schema" {
		t.Fatalf("error.code = %q, want invalid_json_schema; payload=%s", got, payload)
	}
	if got := gjson.Get(payload, "error.param").String(); got != "text.format.schema" {
		t.Fatalf("error.param = %q, want text.format.schema; payload=%s", got, payload)
	}
	message := gjson.Get(payload, "error.message").String()
	if !strings.Contains(message, "issue_exploration_plan") || !strings.Contains(message, "uniqueItems") {
		t.Fatalf("error.message = %q", message)
	}
	if !strings.Contains(message, "('properties', 'targets', 'items', 'properties', 'screen_terms')") {
		t.Fatalf("error.message missing schema context: %q", message)
	}
}

func TestRejectInvalidCodexRequestFormatUniqueItemsInToolSchema(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[],
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"tags":{"type":"array","uniqueItems":true}}}}]
	}`)

	err := rejectInvalidCodexRequestFormat(body)
	if err == nil {
		t.Fatal("expected uniqueItems rejection")
	}
	payload := err.Error()
	if got := gjson.Get(payload, "error.param").String(); got != "tools.0.parameters" {
		t.Fatalf("error.param = %q, want tools.0.parameters; payload=%s", got, payload)
	}
	if !strings.Contains(gjson.Get(payload, "error.message").String(), "lookup") {
		t.Fatalf("error.message = %q", gjson.Get(payload, "error.message").String())
	}
}

func TestRejectInvalidCodexRequestFormatAllowsPlainSchema(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"user","content":"hi"}],
		"text":{"format":{"type":"json_schema","name":"ok","schema":{"type":"object","properties":{"name":{"type":"string"}}}}}
	}`)
	if err := rejectInvalidCodexRequestFormat(body); err != nil {
		t.Fatalf("plain schema should pass: %v", err)
	}
}

func TestCacheHelperRejectsUniqueItemsBeforeHTTP(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[],"text":{"format":{"type":"json_schema","name":"issue_exploration_plan","schema":{"type":"object","properties":{"targets":{"type":"array","items":{"type":"object","properties":{"screen_terms":{"type":"array","uniqueItems":true}}}}}}}}}`)
	exec := &CodexExecutor{}
	req, upstream, _, err := exec.cacheHelper(
		context.Background(),
		sdktranslator.FormatOpenAIResponse,
		"https://example.invalid/v1/responses",
		&cliproxyauth.Auth{ID: "codex-test"},
		cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: body},
		nil,
		body,
	)
	if err == nil {
		t.Fatal("expected uniqueItems rejection")
	}
	if req != nil || upstream != nil {
		t.Fatalf("HTTP request should not be built: req=%v upstream=%s", req, upstream)
	}
	var scoped cliproxyexecutor.RequestScopedError
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("error should be request-scoped: %T %v", err, err)
	}
}

func TestPrepareCodexWebsocketRequestBodyRejectsUniqueItems(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[],"text":{"format":{"type":"json_schema","name":"plan","schema":{"type":"array","uniqueItems":true}}}}`)
	got, err := prepareCodexWebsocketRequestBody(body)
	if err == nil {
		t.Fatalf("expected uniqueItems rejection, got %s", got)
	}
	var scoped cliproxyexecutor.RequestScopedError
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("error should be request-scoped: %T %v", err, err)
	}
}

func TestCacheHelperStripsRejectedInputItemFields(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}],"status":"completed","phase":"final","namespace":"chat"}]}`)
	exec := &CodexExecutor{}
	req, upstream, _, err := exec.cacheHelper(
		context.Background(),
		sdktranslator.FormatOpenAIResponse,
		"https://example.invalid/v1/responses",
		&cliproxyauth.Auth{ID: "codex-test"},
		cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: body},
		nil,
		body,
	)
	if err != nil {
		t.Fatalf("cacheHelper returned error: %v", err)
	}
	if req == nil {
		t.Fatal("expected HTTP request")
	}
	for _, path := range []string{"input.0.status", "input.0.phase", "input.0.namespace"} {
		if gjson.GetBytes(upstream, path).Exists() {
			t.Fatalf("%s should be stripped before HTTP: %s", path, upstream)
		}
	}
	if got := gjson.GetBytes(upstream, "input.0.role").String(); got != "assistant" {
		t.Fatalf("input.0.role = %q, want assistant", got)
	}
}

func TestPrepareCodexWebsocketRequestBodyStripsRejectedInputItemFields(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"assistant","status":"completed","phase":"final","namespace":"chat"}]}`)
	got, err := prepareCodexWebsocketRequestBody(body)
	if err != nil {
		t.Fatalf("prepareCodexWebsocketRequestBody returned error: %v", err)
	}
	for _, path := range []string{"input.0.status", "input.0.phase", "input.0.namespace"} {
		if gjson.GetBytes(got, path).Exists() {
			t.Fatalf("%s should be stripped before websocket: %s", path, got)
		}
	}
	if gotRole := gjson.GetBytes(got, "input.0.role").String(); gotRole != "assistant" {
		t.Fatalf("input.0.role = %q, want assistant", gotRole)
	}
}

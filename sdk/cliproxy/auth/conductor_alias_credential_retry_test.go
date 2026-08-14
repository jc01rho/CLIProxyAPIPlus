package auth

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type aliasCredentialExecutor struct {
	provider string

	mu           sync.Mutex
	executeCalls []string
	streamCalls  []string
	failFor      map[string]error
}

func (e *aliasCredentialExecutor) Identifier() string { return e.provider }

func (e *aliasCredentialExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	call := auth.ID + ":" + req.Model
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, call)
	err := e.failFor[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(call)}, nil
}

func (e *aliasCredentialExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	call := auth.ID + ":" + req.Model
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, call)
	err := e.failFor[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(call)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *aliasCredentialExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *aliasCredentialExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *aliasCredentialExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *aliasCredentialExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func (e *aliasCredentialExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func newAliasCredentialRetryManager(t *testing.T, aliasModel, fallbackModel string) (*Manager, *aliasCredentialExecutor, *aliasCredentialExecutor, *Auth, *Auth, *Auth) {
	t.Helper()
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.SetRetryConfig(0, 0, 1)
	m.SetFallbackChain([]string{fallbackModel}, 1)

	geminiExec := &aliasCredentialExecutor{provider: "gemini", failFor: map[string]error{}}
	fallbackExec := &aliasCredentialExecutor{provider: "fallback", failFor: map[string]error{}}
	m.RegisterExecutor(geminiExec)
	m.RegisterExecutor(fallbackExec)

	first := &Auth{ID: t.Name() + "-gemini-a", Provider: "gemini", Status: StatusActive}
	second := &Auth{ID: t.Name() + "-gemini-b", Provider: "gemini", Status: StatusActive}
	fallbackAuth := &Auth{ID: t.Name() + "-fallback", Provider: "fallback", Status: StatusActive}
	for _, auth := range []*Auth{first, second, fallbackAuth} {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(first.ID, "gemini", []*registry.ModelInfo{{ID: aliasModel}})
	reg.RegisterClient(second.ID, "gemini", []*registry.ModelInfo{{ID: aliasModel}})
	reg.RegisterClient(fallbackAuth.ID, "fallback", []*registry.ModelInfo{{ID: fallbackModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(first.ID)
		reg.UnregisterClient(second.ID)
		reg.UnregisterClient(fallbackAuth.ID)
	})

	return m, geminiExec, fallbackExec, first, second, fallbackAuth
}

func TestManagerExecute_RetriesSameProviderAliasCredentialBeforeFallbackChain(t *testing.T) {
	const aliasModel = "gemini-pro"
	const fallbackModel = "higher-coding"
	m, geminiExec, fallbackExec, first, second, _ := newAliasCredentialRetryManager(t, aliasModel, fallbackModel)
	geminiExec.failFor[first.ID] = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Resource has been exhausted (e.g. check quota)."}

	resp, err := m.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want same-alias credential retry success", err)
	}
	if got := string(resp.Payload); got != second.ID+":"+aliasModel {
		t.Fatalf("payload = %q, want second gemini credential for %s", got, aliasModel)
	}
	if got := geminiExec.ExecuteCalls(); len(got) != 2 || got[0] != first.ID+":"+aliasModel || got[1] != second.ID+":"+aliasModel {
		t.Fatalf("gemini execute calls = %v, want [%s, %s]", got, first.ID+":"+aliasModel, second.ID+":"+aliasModel)
	}
	if got := fallbackExec.ExecuteCalls(); len(got) != 0 {
		t.Fatalf("fallback-chain execute calls = %v, want none before alias credentials are exhausted", got)
	}
}

func TestManagerExecute_UsesFallbackChainAfterSameProviderAliasCredentialsExhausted(t *testing.T) {
	const aliasModel = "gemini-pro"
	const fallbackModel = "higher-coding"
	m, geminiExec, fallbackExec, first, second, fallbackAuth := newAliasCredentialRetryManager(t, aliasModel, fallbackModel)
	quotaErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Resource has been exhausted (e.g. check quota)."}
	geminiExec.failFor[first.ID] = quotaErr
	geminiExec.failFor[second.ID] = quotaErr

	resp, err := m.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want fallback-chain success after alias credentials are exhausted", err)
	}
	if got := string(resp.Payload); got != fallbackAuth.ID+":"+fallbackModel {
		t.Fatalf("payload = %q, want fallback-chain %s", got, fallbackModel)
	}
	if got := geminiExec.ExecuteCalls(); len(got) != 2 {
		t.Fatalf("gemini execute calls = %v, want both alias credentials before fallback-chain", got)
	}
	if got := fallbackExec.ExecuteCalls(); len(got) != 1 || got[0] != fallbackAuth.ID+":"+fallbackModel {
		t.Fatalf("fallback-chain execute calls = %v, want [%s]", got, fallbackAuth.ID+":"+fallbackModel)
	}
}

func TestManagerExecuteStream_RetriesSameProviderAliasCredentialBeforeFallbackChain(t *testing.T) {
	const aliasModel = "gemini-pro"
	const fallbackModel = "higher-coding"
	m, geminiExec, fallbackExec, first, second, _ := newAliasCredentialRetryManager(t, aliasModel, fallbackModel)
	geminiExec.failFor[first.ID] = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Resource has been exhausted (e.g. check quota)."}

	stream, err := m.ExecuteStream(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream error = %v, want same-alias credential retry success", err)
	}
	var payload bytes.Buffer
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload.Write(chunk.Payload)
	}
	if got := payload.String(); got != second.ID+":"+aliasModel {
		t.Fatalf("stream payload = %q, want second gemini credential for %s", got, aliasModel)
	}
	if got := geminiExec.StreamCalls(); len(got) != 2 || got[0] != first.ID+":"+aliasModel || got[1] != second.ID+":"+aliasModel {
		t.Fatalf("gemini stream calls = %v, want [%s, %s]", got, first.ID+":"+aliasModel, second.ID+":"+aliasModel)
	}
	if got := fallbackExec.StreamCalls(); len(got) != 0 {
		t.Fatalf("fallback-chain stream calls = %v, want none before alias credentials are exhausted", got)
	}
}

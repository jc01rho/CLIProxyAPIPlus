package auth

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type aliasModelPoolExecutor struct {
	provider string

	mu           sync.Mutex
	executeCalls []string
	streamCalls  []string
	failFor      map[string]error
}

func (e *aliasModelPoolExecutor) Identifier() string { return e.provider }

func (e *aliasModelPoolExecutor) lookupError(auth *Auth, model string) error {
	if auth == nil {
		return nil
	}
	if err, ok := e.failFor[auth.ID+":"+model]; ok {
		return err
	}
	if err, ok := e.failFor[model]; ok {
		return err
	}
	return e.failFor[auth.ID]
}

func (e *aliasModelPoolExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	call := auth.ID + ":" + req.Model
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, call)
	err := e.lookupError(auth, req.Model)
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(call)}, nil
}

func (e *aliasModelPoolExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	call := auth.ID + ":" + req.Model
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, call)
	err := e.lookupError(auth, req.Model)
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(call)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *aliasModelPoolExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *aliasModelPoolExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *aliasModelPoolExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *aliasModelPoolExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func (e *aliasModelPoolExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func newGeminiAliasModelPoolManager(t *testing.T, aliasModel, fallbackModel, firstUpstream, secondUpstream string) (*Manager, *aliasModelPoolExecutor, *aliasModelPoolExecutor, *Auth, *Auth) {
	t.Helper()

	geminiExec := &aliasModelPoolExecutor{provider: "gemini", failFor: map[string]error{}}
	fallbackExec := &aliasModelPoolExecutor{provider: "fallback", failFor: map[string]error{}}
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(geminiExec)
	manager.RegisterExecutor(fallbackExec)

	const apiKey = "AIza-gemini-alias-pool"
	cfg := &internalconfig.Config{
		GeminiKey: []internalconfig.GeminiKey{{
			APIKey: apiKey,
			Models: []internalconfig.GeminiModel{
				{Name: firstUpstream, Alias: aliasModel},
				{Name: secondUpstream, Alias: aliasModel},
				{Name: "gemini-2.5-flash", Alias: "gemini-flash"},
			},
		}},
	}
	manager.SetConfig(cfg)

	geminiAuth := &Auth{
		ID:         "gemini-key-a",
		Provider:   "gemini",
		Status:     StatusActive,
		Attributes: map[string]string{AttributeAPIKey: apiKey},
	}
	fallbackAuth := &Auth{
		ID:       "fallback-key",
		Provider: "fallback",
		Status:   StatusActive,
		Attributes: map[string]string{
			"model": fallbackModel,
		},
	}
	if _, err := manager.Register(context.Background(), geminiAuth); err != nil {
		t.Fatalf("register gemini auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), fallbackAuth); err != nil {
		t.Fatalf("register fallback auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() {
		reg.UnregisterClient(geminiAuth.ID)
		reg.UnregisterClient(fallbackAuth.ID)
	})
	reg.RegisterClient(geminiAuth.ID, "gemini", []*registry.ModelInfo{
		{ID: aliasModel},
		{ID: firstUpstream},
		{ID: secondUpstream},
		{ID: "gemini-flash"},
		{ID: "gemini-2.5-flash"},
	})
	reg.RegisterClient(fallbackAuth.ID, "fallback", []*registry.ModelInfo{{ID: fallbackModel}})

	manager.SetRetryConfig(0, 0, 1)
	manager.SetFallbackChain([]string{fallbackModel}, 3)
	return manager, geminiExec, fallbackExec, geminiAuth, fallbackAuth
}

func TestResolveConfiguredUpstreamModelPoolGeminiSharedAlias(t *testing.T) {
	cfg := &internalconfig.Config{
		GeminiKey: []internalconfig.GeminiKey{{
			APIKey: "k",
			Models: []internalconfig.GeminiModel{
				{Name: "gemini-2.5-pro", Alias: "gemini-pro"},
				{Name: "gemini-3-pro-preview", Alias: "gemini-pro"},
				{Name: "gemini-2.5-flash", Alias: "gemini-flash"},
			},
		}},
	}
	auth := &Auth{ID: "a1", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "k"}}
	got := resolveConfiguredUpstreamModelPool(cfg, auth, "gemini-pro")
	if len(got) != 2 || got[0] != "gemini-2.5-pro" || got[1] != "gemini-3-pro-preview" {
		t.Fatalf("gemini-pro pool = %v", got)
	}
	flash := resolveConfiguredUpstreamModelPool(cfg, auth, "gemini-flash")
	if len(flash) != 1 || flash[0] != "gemini-2.5-flash" {
		t.Fatalf("gemini-flash pool = %v", flash)
	}
}

func TestExecuteRetriesSameKeyAliasModelsBeforeFallback(t *testing.T) {
	const aliasModel = "gemini-pro"
	const fallbackModel = "higher-coding"
	const firstUpstream = "gemini-2.5-pro"
	const secondUpstream = "gemini-3-pro-preview"

	m, geminiExec, fallbackExec, geminiAuth, fallbackAuth := newGeminiAliasModelPoolManager(t, aliasModel, fallbackModel, firstUpstream, secondUpstream)
	quotaErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Resource has been exhausted (e.g. check quota)."}
	geminiExec.failFor[geminiAuth.ID+":"+firstUpstream] = quotaErr

	resp, err := m.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want same-key alias model retry success", err)
	}
	want := geminiAuth.ID + ":" + secondUpstream
	if !bytes.Equal(resp.Payload, []byte(want)) {
		t.Fatalf("payload = %q, want %q", resp.Payload, want)
	}
	if got := geminiExec.ExecuteCalls(); len(got) != 2 || got[0] != geminiAuth.ID+":"+firstUpstream || got[1] != want {
		t.Fatalf("gemini calls = %v, want [%s %s]", got, geminiAuth.ID+":"+firstUpstream, want)
	}
	if got := fallbackExec.ExecuteCalls(); len(got) != 0 {
		t.Fatalf("fallback calls = %v, want none", got)
	}
	_ = fallbackAuth
}

func TestExecuteStreamRetriesSameKeyAliasModelsBeforeFallback(t *testing.T) {
	const aliasModel = "gemini-pro"
	const fallbackModel = "higher-coding"
	const firstUpstream = "gemini-2.5-pro"
	const secondUpstream = "gemini-3-pro-preview"

	m, geminiExec, fallbackExec, geminiAuth, _ := newGeminiAliasModelPoolManager(t, aliasModel, fallbackModel, firstUpstream, secondUpstream)
	quotaErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Resource has been exhausted (e.g. check quota)."}
	geminiExec.failFor[geminiAuth.ID+":"+firstUpstream] = quotaErr

	stream, err := m.ExecuteStream(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream error = %v, want same-key alias model retry success", err)
	}
	var payload bytes.Buffer
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload.Write(chunk.Payload)
	}
	want := geminiAuth.ID + ":" + secondUpstream
	if payload.String() != want {
		t.Fatalf("stream payload = %q, want %q", payload.String(), want)
	}
	if got := geminiExec.StreamCalls(); len(got) != 2 || got[0] != geminiAuth.ID+":"+firstUpstream || got[1] != want {
		t.Fatalf("gemini stream calls = %v, want both alias models", got)
	}
	if got := fallbackExec.StreamCalls(); len(got) != 0 {
		t.Fatalf("fallback stream calls = %v, want none", got)
	}
}

func TestExecuteFallsBackAfterSameKeyAliasModelsExhausted(t *testing.T) {
	const aliasModel = "gemini-pro"
	const fallbackModel = "higher-coding"
	const firstUpstream = "gemini-2.5-pro"
	const secondUpstream = "gemini-3-pro-preview"

	m, geminiExec, fallbackExec, geminiAuth, fallbackAuth := newGeminiAliasModelPoolManager(t, aliasModel, fallbackModel, firstUpstream, secondUpstream)
	quotaErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Resource has been exhausted (e.g. check quota)."}
	geminiExec.failFor[geminiAuth.ID+":"+firstUpstream] = quotaErr
	geminiExec.failFor[geminiAuth.ID+":"+secondUpstream] = quotaErr

	resp, err := m.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want fallback after alias models exhausted", err)
	}
	want := fallbackAuth.ID + ":" + fallbackModel
	if !bytes.Equal(resp.Payload, []byte(want)) {
		t.Fatalf("payload = %q, want %q", resp.Payload, want)
	}
	if got := geminiExec.ExecuteCalls(); len(got) != 2 {
		t.Fatalf("gemini calls = %v, want both alias models before fallback", got)
	}
	if got := fallbackExec.ExecuteCalls(); len(got) != 1 || got[0] != want {
		t.Fatalf("fallback calls = %v, want [%s]", got, want)
	}
}

func TestQuotaOnOneAliasModelDoesNotBlockOtherModelOnSameKey(t *testing.T) {
	const aliasModel = "gemini-pro"
	const fallbackModel = "higher-coding"
	const firstUpstream = "gemini-2.5-pro"
	const secondUpstream = "gemini-3-pro-preview"

	m, geminiExec, _, geminiAuth, _ := newGeminiAliasModelPoolManager(t, aliasModel, fallbackModel, firstUpstream, secondUpstream)
	quotaErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Resource has been exhausted (e.g. check quota)."}
	geminiExec.failFor[geminiAuth.ID+":"+firstUpstream] = quotaErr

	if _, err := m.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("alias execute error = %v", err)
	}

	resp, err := m.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "gemini-flash"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("flash execute error = %v, quota on %s must not cool the whole key", err, firstUpstream)
	}
	want := geminiAuth.ID + ":gemini-2.5-flash"
	if !bytes.Equal(resp.Payload, []byte(want)) {
		t.Fatalf("flash payload = %q, want %q", resp.Payload, want)
	}
}

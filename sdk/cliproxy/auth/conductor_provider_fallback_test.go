package auth

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type providerFallbackExecutor struct {
	id string

	mu           sync.Mutex
	executeCalls []string
	countCalls   []string
	streamCalls  []string
	executeErr   error
	countErr     error
	streamErr    error
}

func (e *providerFallbackExecutor) Identifier() string { return e.id }

func (e *providerFallbackExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	call := auth.Provider + ":" + auth.ID + ":" + req.Model
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, call)
	err := e.executeErr
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(call)}, nil
}

func (e *providerFallbackExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	call := auth.Provider + ":" + auth.ID + ":" + req.Model
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, call)
	err := e.streamErr
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(call)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Provider": {auth.Provider}}, Chunks: ch}, nil
}

func (e *providerFallbackExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *providerFallbackExecutor) CountTokens(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	call := auth.Provider + ":" + auth.ID + ":" + req.Model
	e.mu.Lock()
	e.countCalls = append(e.countCalls, call)
	err := e.countErr
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(call)}, nil
}

func (e *providerFallbackExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *providerFallbackExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func (e *providerFallbackExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func (e *providerFallbackExecutor) CountCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.countCalls))
	copy(out, e.countCalls)
	return out
}

func newProviderFallbackTestManager(t *testing.T, model string) (*Manager, *providerFallbackExecutor, *providerFallbackExecutor) {
	t.Helper()
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.SetRetryConfig(0, 0, 1)

	first := &providerFallbackExecutor{id: "first"}
	second := &providerFallbackExecutor{id: "second"}
	m.RegisterExecutor(first)
	m.RegisterExecutor(second)

	firstAuth := &Auth{ID: t.Name() + "-first", Provider: "first", Status: StatusActive}
	secondAuth := &Auth{ID: t.Name() + "-second", Provider: "second", Status: StatusActive}
	if _, err := m.Register(context.Background(), firstAuth); err != nil {
		t.Fatalf("register first auth: %v", err)
	}
	if _, err := m.Register(context.Background(), secondAuth); err != nil {
		t.Fatalf("register second auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(firstAuth.ID, "first", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(secondAuth.ID, "second", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(firstAuth.ID)
		reg.UnregisterClient(secondAuth.ID)
	})

	return m, first, second
}

func captureStandardLogger(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	logger := log.StandardLogger()
	originalOut := logger.Out
	originalFormatter := logger.Formatter
	originalLevel := logger.Level

	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	logger.SetFormatter(&log.TextFormatter{DisableTimestamp: true, DisableColors: true})
	logger.SetLevel(log.DebugLevel)

	return buf, func() {
		logger.SetOutput(originalOut)
		logger.SetFormatter(originalFormatter)
		logger.SetLevel(originalLevel)
	}
}

func TestManagerExecute_FallsBackToOtherProviderOn429WhenRetryBudgetIsOne(t *testing.T) {
	const model = "glm-5.1"
	m, first, second := newProviderFallbackTestManager(t, model)
	first.executeErr = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}
	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	resp, err := m.Execute(context.Background(), []string{"first", "second"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want success", err)
	}
	if got := string(resp.Payload); got != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("payload = %q, want second provider success", got)
	}

	if got := first.ExecuteCalls(); len(got) != 1 || got[0] != "first:"+t.Name()+"-first:"+model {
		t.Fatalf("first execute calls = %v", got)
	}
	if got := second.ExecuteCalls(); len(got) != 1 || got[0] != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("second execute calls = %v", got)
	}
	if got := logBuf.String(); !bytes.Contains([]byte(got), []byte("retrying with another untried provider")) || !bytes.Contains([]byte(got), []byte("status 429")) {
		t.Fatalf("fallback log = %q, want retry log mentioning status 429", got)
	}
}

func TestManagerExecute_FallsBackToOtherProviderOn504WhenRetryBudgetIsOne(t *testing.T) {
	const model = "glm-5.1"
	m, first, second := newProviderFallbackTestManager(t, model)
	first.executeErr = &Error{HTTPStatus: http.StatusGatewayTimeout, Message: "gateway timeout"}
	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	resp, err := m.Execute(context.Background(), []string{"first", "second"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want success", err)
	}
	if got := string(resp.Payload); got != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("payload = %q, want second provider success", got)
	}
	if got := first.ExecuteCalls(); len(got) != 1 || got[0] != "first:"+t.Name()+"-first:"+model {
		t.Fatalf("first execute calls = %v", got)
	}
	if got := second.ExecuteCalls(); len(got) != 1 || got[0] != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("second execute calls = %v", got)
	}
	if got := logBuf.String(); !bytes.Contains([]byte(got), []byte("retrying with another untried provider")) || !bytes.Contains([]byte(got), []byte("status 504")) {
		t.Fatalf("fallback log = %q, want retry log mentioning status 504", got)
	}
}

func TestManagerExecuteStream_FallsBackToOtherProviderOn429WhenRetryBudgetIsOne(t *testing.T) {
	const model = "glm-5.1"
	m, first, second := newProviderFallbackTestManager(t, model)
	first.streamErr = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}

	streamResult, err := m.ExecuteStream(context.Background(), []string{"first", "second"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream error = %v, want success", err)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if got := string(payload); got != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("payload = %q, want second provider success", got)
	}
	if got := streamResult.Headers.Get("X-Provider"); got != "second" {
		t.Fatalf("X-Provider = %q, want second", got)
	}

	if got := first.StreamCalls(); len(got) != 1 || got[0] != "first:"+t.Name()+"-first:"+model {
		t.Fatalf("first stream calls = %v", got)
	}
	if got := second.StreamCalls(); len(got) != 1 || got[0] != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("second stream calls = %v", got)
	}
}

func TestManagerExecuteStream_FallsBackToOtherProviderOn504WhenRetryBudgetIsOne(t *testing.T) {
	const model = "glm-5.1"
	m, first, _ := newProviderFallbackTestManager(t, model)
	first.streamErr = &Error{HTTPStatus: http.StatusGatewayTimeout, Message: "gateway timeout"}

	streamResult, err := m.ExecuteStream(context.Background(), []string{"first", "second"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream error = %v, want success", err)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if got := string(payload); got != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("payload = %q, want second provider success", got)
	}
	if got := streamResult.Headers.Get("X-Provider"); got != "second" {
		t.Fatalf("X-Provider = %q, want second", got)
	}
}

func TestManagerExecuteCount_FallsBackToOtherProviderOn429WhenRetryBudgetIsOne(t *testing.T) {
	const model = "glm-5.1"
	m, first, second := newProviderFallbackTestManager(t, model)
	first.countErr = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}

	resp, err := m.ExecuteCount(context.Background(), []string{"first", "second"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute count error = %v, want success", err)
	}
	if got := string(resp.Payload); got != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("payload = %q, want second provider success", got)
	}
	if got := first.CountCalls(); len(got) != 1 || got[0] != "first:"+t.Name()+"-first:"+model {
		t.Fatalf("first count calls = %v", got)
	}
	if got := second.CountCalls(); len(got) != 1 || got[0] != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("second count calls = %v", got)
	}
}

func TestManagerExecuteCount_FallsBackToOtherProviderOn504WhenRetryBudgetIsOne(t *testing.T) {
	const model = "glm-5.1"
	m, first, second := newProviderFallbackTestManager(t, model)
	first.countErr = &Error{HTTPStatus: http.StatusGatewayTimeout, Message: "gateway timeout"}

	resp, err := m.ExecuteCount(context.Background(), []string{"first", "second"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute count error = %v, want success", err)
	}
	if got := string(resp.Payload); got != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("payload = %q, want second provider success", got)
	}
	if got := first.CountCalls(); len(got) != 1 || got[0] != "first:"+t.Name()+"-first:"+model {
		t.Fatalf("first count calls = %v", got)
	}
	if got := second.CountCalls(); len(got) != 1 || got[0] != "second:"+t.Name()+"-second:"+model {
		t.Fatalf("second count calls = %v", got)
	}
}

func TestManagerExecute_FallsBackAcrossCompatProviderKeysOn429(t *testing.T) {
	const model = "minimax-kimi"
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.SetRetryConfig(0, 0, 1)

	first := &providerFallbackExecutor{id: "aperties-free"}
	second := &providerFallbackExecutor{id: "openrouter-free"}
	first.executeErr = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
	m.RegisterExecutor(first)
	m.RegisterExecutor(second)

	firstAuth := &Auth{
		ID:       t.Name() + "-first",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Metadata: map[string]any{"kind": "api_key"},
		Attributes: map[string]string{
			"provider_key": "aperties-free",
			"compat_name":  "aperties-free",
			"api_key":      "test-aperties-free",
		},
	}
	secondAuth := &Auth{
		ID:       t.Name() + "-second",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Metadata: map[string]any{"kind": "api_key"},
		Attributes: map[string]string{
			"provider_key": "openrouter-free",
			"compat_name":  "openrouter-free",
			"api_key":      "test-openrouter-free",
		},
	}
	if _, err := m.Register(context.Background(), firstAuth); err != nil {
		t.Fatalf("register first compat auth: %v", err)
	}
	if _, err := m.Register(context.Background(), secondAuth); err != nil {
		t.Fatalf("register second compat auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(firstAuth.ID, "aperties-free", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(secondAuth.ID, "openrouter-free", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(firstAuth.ID)
		reg.UnregisterClient(secondAuth.ID)
	})

	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	resp, err := m.Execute(context.Background(), []string{"aperties-free", "openrouter-free"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want compat fallback success", err)
	}
	if got := string(resp.Payload); got != "openai-compatibility:"+t.Name()+"-second:"+model {
		t.Fatalf("payload = %q, want second compat provider success", got)
	}
	if got := first.ExecuteCalls(); len(got) != 1 || got[0] != "openai-compatibility:"+t.Name()+"-first:"+model {
		t.Fatalf("first execute calls = %v", got)
	}
	if got := second.ExecuteCalls(); len(got) != 1 || got[0] != "openai-compatibility:"+t.Name()+"-second:"+model {
		t.Fatalf("second execute calls = %v", got)
	}
	if got := logBuf.String(); !bytes.Contains([]byte(got), []byte("provider aperties-free failed with upstream status 429")) {
		t.Fatalf("fallback log = %q, want compat provider key retry log", got)
	}
}

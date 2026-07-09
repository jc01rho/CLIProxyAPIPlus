package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerExecuteStreamWithRouteFallback_returnsCancellationWithoutTryingFallbackModel(t *testing.T) {
	// Given
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"lower-coding"}, 1)
	request := cliproxyexecutor.Request{Model: "gpt-5.5"}
	var attemptedModels []string

	execOnce := func(
		ctx context.Context,
		providers []string,
		req cliproxyexecutor.Request,
		opts cliproxyexecutor.Options,
		maxRetryCredentials int,
	) (*cliproxyexecutor.StreamResult, error) {
		attemptedModels = append(attemptedModels, req.Model)
		return nil, context.Canceled
	}

	// When
	_, err := manager.executeStreamWithRouteFallback(
		context.Background(),
		[]string{"codex"},
		request,
		cliproxyexecutor.Options{},
		execOnce,
	)

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeStreamWithRouteFallback() error = %v, want context.Canceled", err)
	}
	if len(attemptedModels) != 1 || attemptedModels[0] != "gpt-5.5" {
		t.Fatalf("attempted models = %v, want only [gpt-5.5]", attemptedModels)
	}
}

func TestManagerExecuteWithRouteFallback_AllowsConsoleUpstreamRequestFailed(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"big-pickle"}, 1)
	request := cliproxyexecutor.Request{Model: "deepseek-v4-flash-free"}
	upstreamErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `invalid_request_error: Error from provider (Console): Upstream request failed`,
	}
	var attemptedModels []string

	execOnce := func(
		ctx context.Context,
		providers []string,
		req cliproxyexecutor.Request,
		opts cliproxyexecutor.Options,
		maxRetryCredentials int,
	) (cliproxyexecutor.Response, error) {
		_ = ctx
		_ = providers
		_ = opts
		_ = maxRetryCredentials
		attemptedModels = append(attemptedModels, req.Model)
		if req.Model == "deepseek-v4-flash-free" {
			return cliproxyexecutor.Response{}, upstreamErr
		}
		return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
	}

	resp, err := manager.executeWithRouteFallback(
		context.Background(),
		[]string{"openai-compatible-opencode-free"},
		request,
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != nil {
		t.Fatalf("executeWithRouteFallback() error = %v, want fallback success", err)
	}
	if string(resp.Payload) != "big-pickle" {
		t.Fatalf("payload = %q, want big-pickle", string(resp.Payload))
	}
	wantModels := []string{"deepseek-v4-flash-free", "big-pickle"}
	if len(attemptedModels) != len(wantModels) {
		t.Fatalf("attempted models = %v, want %v", attemptedModels, wantModels)
	}
	for i := range wantModels {
		if attemptedModels[i] != wantModels[i] {
			t.Fatalf("attempted model %d = %q, want %q", i, attemptedModels[i], wantModels[i])
		}
	}
}

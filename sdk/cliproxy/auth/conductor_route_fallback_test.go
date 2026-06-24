package auth

import (
	"context"
	"errors"
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

package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// System One models (jev, clef) serve only native /v1/systemone payloads, so a
// failed System One request must fall back to another System One model and
// never to the chat fallback chain, and chat requests must never land on them.

func runSystemOneRouteFallback(t *testing.T, manager *Manager, model, alt string) []string {
	t.Helper()
	var attempted []string
	execOnce := func(
		ctx context.Context,
		providers []string,
		req cliproxyexecutor.Request,
		opts cliproxyexecutor.Options,
		maxRetryCredentials int,
		retryRound int,
		defaultRequestRetry int,
	) (cliproxyexecutor.Response, error) {
		attempted = append(attempted, req.Model)
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream unavailable"}
	}
	_, err := manager.executeWithRouteFallback(
		context.Background(),
		[]string{"openai-compatibility"},
		cliproxyexecutor.Request{Model: model},
		cliproxyexecutor.Options{Alt: alt},
		execOnce,
	)
	if err == nil {
		t.Fatalf("executeWithRouteFallback(%q) error = nil, want upstream failure", model)
	}
	return attempted
}

func registerSystemOneFallbackModels(t *testing.T, ids ...string) {
	t.Helper()
	infos := make([]*registry.ModelInfo, 0, len(ids))
	for _, id := range ids {
		infos = append(infos, &registry.ModelInfo{ID: id})
	}
	registry.GetGlobalRegistry().RegisterClient("systemone-fallback-test", "openai-compatibility", infos)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("systemone-fallback-test") })
}

func TestSystemOneRouteFallback_StaysWithinSystemOneModels(t *testing.T) {
	registerSystemOneFallbackModels(t, "jev-latest", "clef", "claude-opus-5-5", "gpt-6.1-sol")
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-opus-5-5", "gpt-6.1-sol"}, 10)

	for _, tc := range []struct {
		model string
		want  []string
	}{
		{model: "jev-latest", want: []string{"jev-latest", "clef"}},
		{model: "clef", want: []string{"clef", "jev-latest"}},
	} {
		got := runSystemOneRouteFallback(t, manager, tc.model, "systemone")
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s attempted models = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestSystemOneRouteFallback_ChatRequestSkipsSystemOneModels(t *testing.T) {
	registerSystemOneFallbackModels(t, "jev-latest", "clef", "gpt-6.1-sol")
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"jev-latest", "clef", "gpt-6.1-sol"}, 10)

	got := runSystemOneRouteFallback(t, manager, "claude-opus-5-5", "")
	want := []string{"claude-opus-5-5", "gpt-6.1-sol"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attempted models = %v, want %v", got, want)
	}
}

func TestIsSystemOneModelName(t *testing.T) {
	for name, want := range map[string]bool{
		"jev-latest":      true,
		"jev-1.13-free":   true,
		"typesafe-ai/jev": true,
		"clef":            true,
		"clef-flash":      true,
		"claude-opus-5-5": false,
		"higher-coding":   false,
	} {
		if got := isSystemOneModelName(name); got != want {
			t.Errorf("isSystemOneModelName(%q) = %v, want %v", name, got, want)
		}
	}
}

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// contextWithModelWhitelist builds a request context shaped like the one the API
// layer hands to the auth manager: a gin context carrying the authenticated API
// key's model whitelist, reachable through the "gin" context value.
func contextWithModelWhitelist(patterns []string) context.Context {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	sdkaccess.SetModelAccessPatterns(ginCtx, patterns)
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestResolveFallbackModels_SkipsModelsOutsideAPIKeyWhitelist(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackModels(map[string]string{"gpt-5.5": "claude-opus-4"})
	manager.SetFallbackChain([]string{"gpt-4.1-mini", "claude-sonnet-4", "gpt-4o"}, 5)

	ctx := contextWithModelWhitelist([]string{"gpt-*"})
	got := manager.resolveFallbackModels(ctx, "gpt-5.5")

	want := []string{"gpt-4.1-mini", "gpt-4o"}
	if len(got) != len(want) {
		t.Fatalf("fallback candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestResolveFallbackModels_UnrestrictedKeyKeepsEveryCandidate(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackModels(map[string]string{"gpt-5.5": "claude-opus-4"})
	manager.SetFallbackChain([]string{"gpt-4.1-mini", "claude-sonnet-4"}, 5)

	want := []string{"claude-opus-4", "gpt-4.1-mini", "claude-sonnet-4"}

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "no gin context", ctx: context.Background()},
		{name: "nil context", ctx: nil},
		{name: "empty whitelist", ctx: contextWithModelWhitelist(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := manager.resolveFallbackModels(tc.ctx, "gpt-5.5")
			if len(got) != len(want) {
				t.Fatalf("fallback candidates = %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("candidate %d = %q, want %q (all: %v)", i, got[i], want[i], got)
				}
			}
		})
	}
}

func TestResolveFallbackModels_WhitelistBlockingEveryCandidateYieldsNone(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-sonnet-4", "claude-opus-4"}, 5)

	ctx := contextWithModelWhitelist([]string{"gpt-*"})
	if got := manager.resolveFallbackModels(ctx, "gpt-5.5"); len(got) != 0 {
		t.Fatalf("fallback candidates = %v, want none", got)
	}
}

func TestResolveFallbackModels_DepthCapAppliesAfterWhitelistFiltering(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-a", "claude-b", "gpt-4.1", "gpt-4o"}, 2)

	ctx := contextWithModelWhitelist([]string{"gpt-*"})
	got := manager.resolveFallbackModels(ctx, "gpt-5.5")

	// Without whitelist-aware filtering the two Claude entries would consume the
	// depth budget and starve the models this key is actually allowed to use.
	want := []string{"gpt-4.1", "gpt-4o"}
	if len(got) != len(want) {
		t.Fatalf("fallback candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestExecuteWithRouteFallback_DoesNotReachModelBlockedForAPIKey(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-sonnet-4", "gpt-4o"}, 5)

	upstreamErr := &Error{HTTPStatus: http.StatusBadRequest, Message: "upstream failed"}
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
		if req.Model == "gpt-5.5" {
			return cliproxyexecutor.Response{}, upstreamErr
		}
		return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
	}

	ctx := contextWithModelWhitelist([]string{"gpt-*"})
	resp, err := manager.executeWithRouteFallback(
		ctx,
		[]string{"openai"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != nil {
		t.Fatalf("executeWithRouteFallback() error = %v, want fallback success", err)
	}
	if string(resp.Payload) != "gpt-4o" {
		t.Fatalf("payload = %q, want gpt-4o", string(resp.Payload))
	}
	want := []string{"gpt-5.5", "gpt-4o"}
	if len(attempted) != len(want) {
		t.Fatalf("attempted models = %v, want %v", attempted, want)
	}
	for i := range want {
		if attempted[i] != want[i] {
			t.Fatalf("attempted model %d = %q, want %q", i, attempted[i], want[i])
		}
	}
	for _, model := range attempted {
		if model == "claude-sonnet-4" {
			t.Fatalf("blocked model claude-sonnet-4 was executed: %v", attempted)
		}
	}
}

func TestExecuteStreamWithRouteFallback_DoesNotReachModelBlockedForAPIKey(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-sonnet-4"}, 5)

	upstreamErr := &Error{HTTPStatus: http.StatusBadRequest, Message: "upstream failed"}
	var attempted []string
	execOnce := func(
		ctx context.Context,
		providers []string,
		req cliproxyexecutor.Request,
		opts cliproxyexecutor.Options,
		maxRetryCredentials int,
		homeRetryLimit *int,
		retryRound int,
		defaultRequestRetry int,
	) (*cliproxyexecutor.StreamResult, error) {
		attempted = append(attempted, req.Model)
		return nil, upstreamErr
	}

	ctx := contextWithModelWhitelist([]string{"gpt-*"})
	if _, err := manager.executeStreamWithRouteFallback(
		ctx,
		[]string{"openai"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	); err == nil {
		t.Fatal("executeStreamWithRouteFallback() error = nil, want upstream error")
	}
	if len(attempted) != 1 || attempted[0] != "gpt-5.5" {
		t.Fatalf("attempted models = %v, want only [gpt-5.5]", attempted)
	}
}

func TestResolveProvidersForFallback_RespectsAPIKeyWhitelist(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-sonnet-4"}, 5)

	ctx := contextWithModelWhitelist([]string{"gpt-*"})
	providers, fallbackModel := manager.ResolveProvidersForFallback(ctx, "gpt-5.5")
	if len(providers) != 0 || fallbackModel != "" {
		t.Fatalf("providers = %v, model = %q, want none for a blocked fallback", providers, fallbackModel)
	}
}

func TestFallbackModelAllowed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns []string
		model    string
		want     bool
	}{
		{name: "no patterns allows everything", patterns: nil, model: "claude-opus-4", want: true},
		{name: "exact match", patterns: []string{"gpt-4o"}, model: "gpt-4o", want: true},
		{name: "wildcard match", patterns: []string{"gpt-*"}, model: "gpt-4.1-mini", want: true},
		{name: "no match", patterns: []string{"gpt-*"}, model: "claude-sonnet-4", want: false},
		{name: "prefix is not a substring match", patterns: []string{"gpt-4o"}, model: "gpt-4o-mini", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fallbackModelAllowed(tc.patterns, tc.model); got != tc.want {
				t.Fatalf("fallbackModelAllowed(%v, %q) = %v, want %v", tc.patterns, tc.model, got, tc.want)
			}
		})
	}
}

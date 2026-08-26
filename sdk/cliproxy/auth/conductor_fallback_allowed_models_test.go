package auth

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestFallbackAllowlistMatches covers direct and registry-resolved actual-model
// matching, case-insensitivity, and trimming.
func TestFallbackAllowlistMatches(t *testing.T) {
	registry.GetGlobalRegistry().RegisterClient("fallback-allowlist-alias-test", "codex", []*registry.ModelInfo{
		{ID: "gpt-5.5-alias", Alias: "gpt-5.5-upstream", ExecutionTarget: "gpt-5.5-upstream"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("fallback-allowlist-alias-test") })

	for _, tc := range []struct {
		name    string
		allowed []string
		model   string
		want    bool
	}{
		{name: "empty allowlist denies (handled separately by caller)", allowed: nil, model: "gpt-4o", want: false},
		{name: "direct match", allowed: []string{"gpt-4o"}, model: "gpt-4o", want: true},
		{name: "case insensitive and trimmed", allowed: []string{"  GPT-4O  "}, model: "gpt-4o", want: true},
		{name: "no match", allowed: []string{"claude-opus-4"}, model: "gpt-4o", want: false},
		{name: "requested alias matches allowlisted actual model", allowed: []string{"gpt-5.5-upstream"}, model: "gpt-5.5-alias", want: true},
		{name: "requested actual model matches allowlisted alias", allowed: []string{"gpt-5.5-alias"}, model: "gpt-5.5-upstream", want: true},
		{name: "unknown model name still matches directly", allowed: []string{"totally-unknown-model"}, model: "totally-unknown-model", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fallbackAllowlistMatches(tc.allowed, tc.model); got != tc.want {
				t.Fatalf("fallbackAllowlistMatches(%v, %q) = %v, want %v", tc.allowed, tc.model, got, tc.want)
			}
		})
	}
}

// TestResolveActualModelNameIsRegistryIDOnly proves resolveActualModelName
// (used by unrelated API-key whitelist and fallback-logging callers) resolves
// only registered model IDs and does not scan for alias strings. Alias-string
// scanning belongs exclusively to the policy-specific helper.
func TestResolveActualModelNameIsRegistryIDOnly(t *testing.T) {
	registry.GetGlobalRegistry().RegisterClient("resolve-actual-model-name-test", "codex", []*registry.ModelInfo{
		{ID: "gpt-5.5-id", Alias: "gpt-5.5-alias-only", ExecutionTarget: "gpt-5.5-upstream"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("resolve-actual-model-name-test") })

	// Resolving by registered ID still works and reports the alias resolution.
	if got := resolveActualModelName("gpt-5.5-id"); !got.isAlias || got.actual != "gpt-5.5-upstream" {
		t.Fatalf("resolveActualModelName(%q) = %+v, want actual=gpt-5.5-upstream isAlias=true", "gpt-5.5-id", got)
	}
	// Resolving by the bare alias string must NOT resolve: registry lookups
	// are ID-only for this function.
	if got := resolveActualModelName("gpt-5.5-alias-only"); got.actual != "" || got.isAlias {
		t.Fatalf("resolveActualModelName(%q) = %+v, want empty/unknown result", "gpt-5.5-alias-only", got)
	}
}

// TestFallbackAllowedModelsReturnsDefensiveCopy proves the public getter
// returns a copy that callers may mutate without affecting the manager's
// stored policy or subsequent getter calls.
func TestFallbackAllowedModelsReturnsDefensiveCopy(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackAllowedModels([]string{"gpt-4o", "claude-opus-4"})

	got := manager.FallbackAllowedModels()
	if len(got) != 2 || got[0] != "gpt-4o" || got[1] != "claude-opus-4" {
		t.Fatalf("FallbackAllowedModels() = %v, want [gpt-4o claude-opus-4]", got)
	}
	got[0] = "mutated"
	got = manager.FallbackAllowedModels()
	if got[0] != "gpt-4o" {
		t.Fatalf("FallbackAllowedModels() after external mutation = %v, want unaffected [gpt-4o claude-opus-4]", got)
	}
}

// TestManagerSetFallbackAllowedModels_EmptyPreservesRetryAndChain proves that a
// nil/empty policy preserves all existing retry and fallback-chain behavior.
func TestManagerSetFallbackAllowedModels_EmptyPreservesRetryAndChain(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-opus-4"}, 1)
	// No SetFallbackAllowedModels call: policy stays nil/empty.

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

	resp, err := manager.executeWithRouteFallback(
		context.Background(),
		[]string{"openai"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != nil {
		t.Fatalf("executeWithRouteFallback() error = %v, want fallback success", err)
	}
	if string(resp.Payload) != "claude-opus-4" {
		t.Fatalf("payload = %q, want claude-opus-4", string(resp.Payload))
	}
	want := []string{"gpt-5.5", "claude-opus-4"}
	if len(attempted) != len(want) {
		t.Fatalf("attempted models = %v, want %v", attempted, want)
	}
	for i := range want {
		if attempted[i] != want[i] {
			t.Fatalf("attempted model %d = %q, want %q", i, attempted[i], want[i])
		}
	}
}

// TestManagerSetFallbackAllowedModels_AllowedAliasPermitsRetryAndChain proves
// that a requested model matching the allowlist through its registry-resolved
// actual model still permits credential retry and the fallback chain.
func TestManagerSetFallbackAllowedModels_AllowedAliasPermitsRetryAndChain(t *testing.T) {
	registry.GetGlobalRegistry().RegisterClient("fallback-allowlist-permit-test", "codex", []*registry.ModelInfo{
		{ID: "gpt-5.5-requested", Alias: "gpt-5.5-upstream", ExecutionTarget: "gpt-5.5-upstream"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("fallback-allowlist-permit-test") })

	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-opus-4"}, 1)
	// Allowlist names the upstream (actual) model; the request uses the alias.
	manager.SetFallbackAllowedModels([]string{"gpt-5.5-upstream"})

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
		if req.Model == "gpt-5.5-requested" {
			return cliproxyexecutor.Response{}, upstreamErr
		}
		return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
	}

	resp, err := manager.executeWithRouteFallback(
		context.Background(),
		[]string{"openai"},
		cliproxyexecutor.Request{Model: "gpt-5.5-requested"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != nil {
		t.Fatalf("executeWithRouteFallback() error = %v, want fallback success", err)
	}
	if string(resp.Payload) != "claude-opus-4" {
		t.Fatalf("payload = %q, want claude-opus-4", string(resp.Payload))
	}
	want := []string{"gpt-5.5-requested", "claude-opus-4"}
	if len(attempted) != len(want) {
		t.Fatalf("attempted models = %v, want %v", attempted, want)
	}
	for i := range want {
		if attempted[i] != want[i] {
			t.Fatalf("attempted model %d = %q, want %q", i, attempted[i], want[i])
		}
	}
}

// TestManagerSetFallbackAllowedModels_UnlistedModelMakesOneCallOnly proves that
// a nonempty allowlist denies both credential retry and fallback-chain entry
// for an unlisted requested model: exactly one initial call is made and the
// original error is returned unchanged.
func TestManagerSetFallbackAllowedModels_UnlistedModelMakesOneCallOnly(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-opus-4"}, 1)
	manager.SetFallbackAllowedModels([]string{"gpt-4o"})

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
		return cliproxyexecutor.Response{}, upstreamErr
	}

	_, err := manager.executeWithRouteFallback(
		context.Background(),
		[]string{"openai"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != upstreamErr {
		t.Fatalf("executeWithRouteFallback() error = %v, want original upstream error %v", err, upstreamErr)
	}
	if len(attempted) != 1 || attempted[0] != "gpt-5.5" {
		t.Fatalf("attempted models = %v, want exactly one initial call [gpt-5.5]", attempted)
	}
}

// TestManagerSetFallbackAllowedModels_StreamUnlistedModelMakesOneCallOnly
// covers the streaming path equivalent of the gating behavior above.
func TestManagerSetFallbackAllowedModels_StreamUnlistedModelMakesOneCallOnly(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-opus-4"}, 1)
	manager.SetFallbackAllowedModels([]string{"gpt-4o"})

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

	_, err := manager.executeStreamWithRouteFallback(
		context.Background(),
		[]string{"openai"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != upstreamErr {
		t.Fatalf("executeStreamWithRouteFallback() error = %v, want original upstream error %v", err, upstreamErr)
	}
	if len(attempted) != 1 || attempted[0] != "gpt-5.5" {
		t.Fatalf("attempted models = %v, want exactly one initial call [gpt-5.5]", attempted)
	}
}

// TestManagerSetFallbackAllowedModels_StreamAllowedModelPermitsChain proves the
// streaming path permits the fallback chain when the requested model matches
// the allowlist directly.
func TestManagerSetFallbackAllowedModels_StreamAllowedModelPermitsChain(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"claude-opus-4"}, 1)
	manager.SetFallbackAllowedModels([]string{"gpt-5.5"})

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
		if req.Model == "gpt-5.5" {
			return nil, upstreamErr
		}
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Model": {req.Model}}}, nil
	}

	result, err := manager.executeStreamWithRouteFallback(
		context.Background(),
		[]string{"openai"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != nil {
		t.Fatalf("executeStreamWithRouteFallback() error = %v, want fallback success", err)
	}
	if result == nil || result.Headers.Get("X-Model") != "claude-opus-4" {
		t.Fatalf("result = %+v, want fallback model claude-opus-4 to have executed", result)
	}
	want := []string{"gpt-5.5", "claude-opus-4"}
	if len(attempted) != len(want) {
		t.Fatalf("attempted models = %v, want %v", attempted, want)
	}
	for i := range want {
		if attempted[i] != want[i] {
			t.Fatalf("attempted model %d = %q, want %q", i, attempted[i], want[i])
		}
	}
}

// TestManagerSetFallbackAllowedModels_DeniesCredentialRetryWithoutFallbackChain
// proves the policy also gates post-error credential retry directly: with a
// denied model and no fallback chain configured at all, only one call happens.
func TestManagerSetFallbackAllowedModels_DeniesCredentialRetryWithoutFallbackChain(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	// No fallback chain/models configured; only credential retry could apply.
	manager.SetFallbackAllowedModels([]string{"gpt-4o"})

	upstreamErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
	callCount := 0
	execOnce := func(
		ctx context.Context,
		providers []string,
		req cliproxyexecutor.Request,
		opts cliproxyexecutor.Options,
		maxRetryCredentials int,
		retryRound int,
		defaultRequestRetry int,
	) (cliproxyexecutor.Response, error) {
		callCount++
		return cliproxyexecutor.Response{}, upstreamErr
	}

	_, err := manager.executeWithRouteFallback(
		context.Background(),
		[]string{"openai", "openai-2"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != upstreamErr {
		t.Fatalf("executeWithRouteFallback() error = %v, want original upstream error %v", err, upstreamErr)
	}
	if callCount != 1 {
		t.Fatalf("execOnce call count = %d, want exactly 1 (no post-error credential retry)", callCount)
	}
}

// TestManagerSetFallbackAllowedModels_DeniedModelIgnoresConfiguredRequestRetry
// is the key regression test for defect 1: even with a nonzero request-retry
// count configured (which drives a second attempt round independently of the
// per-round credential limit), a denied model must still make exactly one
// call. maxRetryCredentials=0 alone does not stop request-level retry rounds,
// so the policy must be enforced inside the retry loop itself, immediately
// after the first error.
func TestManagerSetFallbackAllowedModels_DeniedModelIgnoresConfiguredRequestRetry(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	// Configure a nonzero request-retry count and a nonzero credential-retry
	// limit so that, absent the policy gate, a second attempt round would run.
	manager.SetRetryConfig(3, 0, 5)
	manager.SetFallbackAllowedModels([]string{"gpt-4o"})

	upstreamErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
	callCount := 0
	execOnce := func(
		ctx context.Context,
		providers []string,
		req cliproxyexecutor.Request,
		opts cliproxyexecutor.Options,
		maxRetryCredentials int,
		retryRound int,
		defaultRequestRetry int,
	) (cliproxyexecutor.Response, error) {
		callCount++
		return cliproxyexecutor.Response{}, upstreamErr
	}

	_, err := manager.executeWithRouteFallback(
		context.Background(),
		[]string{"openai", "openai-2"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != upstreamErr {
		t.Fatalf("executeWithRouteFallback() error = %v, want original upstream error %v", err, upstreamErr)
	}
	if callCount != 1 {
		t.Fatalf("execOnce call count = %d, want exactly 1 despite nonzero request-retry configuration", callCount)
	}
}

// TestManagerSetConfig_FallbackAllowedModelsActivatesImmediatelyOnStartup is the
// regression test for the missed startup path: CLIProxyAPIPlus/sdk/cliproxy/builder.go
// installs the routing policy only via Manager.SetConfig during startup (the
// management Handler.SetConfig/SetAuthManager wiring only runs on reload), so
// Manager.SetConfig itself must activate a nonempty Routing.FallbackAllowedModels
// policy immediately, with no separate reload step. This proves an unlisted
// model makes exactly one failed execution even with request-retry configured,
// mirroring TestManagerSetFallbackAllowedModels_DeniedModelIgnoresConfiguredRequestRetry
// but driving the policy through the config lifecycle seam instead of the
// setter directly.
func TestManagerSetConfig_FallbackAllowedModelsActivatesImmediatelyOnStartup(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	// A single Manager.SetConfig call, as performed once at startup by
	// sdk/cliproxy/builder.go, with no follow-up management reload.
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			FallbackChain:         []string{"claude-opus-4"},
			FallbackMaxDepth:      1,
			FallbackAllowedModels: []string{"gpt-4o"},
		},
	})
	// Configure a nonzero request-retry count so that, absent the policy gate
	// activating from SetConfig alone, a second attempt round would run.
	manager.SetRetryConfig(3, 0, 5)

	upstreamErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
	callCount := 0
	execOnce := func(
		ctx context.Context,
		providers []string,
		req cliproxyexecutor.Request,
		opts cliproxyexecutor.Options,
		maxRetryCredentials int,
		retryRound int,
		defaultRequestRetry int,
	) (cliproxyexecutor.Response, error) {
		callCount++
		return cliproxyexecutor.Response{}, upstreamErr
	}

	_, err := manager.executeWithRouteFallback(
		context.Background(),
		[]string{"openai", "openai-2"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != upstreamErr {
		t.Fatalf("executeWithRouteFallback() error = %v, want original upstream error %v", err, upstreamErr)
	}
	if callCount != 1 {
		t.Fatalf("execOnce call count = %d, want exactly 1: Manager.SetConfig must activate the fallback-allowed-models policy immediately, without a management reload", callCount)
	}
}

// TestManagerSetFallbackAllowedModels_StreamDeniedModelIgnoresConfiguredRequestRetry
// covers the streaming-path equivalent of the regression test above.
func TestManagerSetFallbackAllowedModels_StreamDeniedModelIgnoresConfiguredRequestRetry(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 0, 5)
	manager.SetFallbackAllowedModels([]string{"gpt-4o"})

	upstreamErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
	callCount := 0
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
		callCount++
		return nil, upstreamErr
	}

	_, err := manager.executeStreamWithRouteFallback(
		context.Background(),
		[]string{"openai", "openai-2"},
		cliproxyexecutor.Request{Model: "gpt-5.5"},
		cliproxyexecutor.Options{},
		execOnce,
	)
	if err != upstreamErr {
		t.Fatalf("executeStreamWithRouteFallback() error = %v, want original upstream error %v", err, upstreamErr)
	}
	if callCount != 1 {
		t.Fatalf("execOnce call count = %d, want exactly 1 despite nonzero request-retry configuration", callCount)
	}
}

package executor

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func resetAntigravityTokenTracker(t *testing.T) {
	t.Helper()
	reset := func() {
		antigravity.InitTokenTracker(antigravity.TokenBucketConfig{})
		antigravityLimiterConfiguredFromYAML.Store(false)
	}
	reset()
	t.Cleanup(reset)
}

func drainAntigravityBucket(cfg *config.Config, auth *cliproxyauth.Auth) int {
	n := 0
	for antigravityEnsureRequestTokens(cfg, auth, nil) == nil {
		antigravityConsumeRequestTokens(cfg, auth, nil)
		n++
		if n > 100000 {
			break
		}
	}
	return n
}

func TestAntigravityLocalRateLimitDefaultsMatchHistoricalBucket(t *testing.T) {
	resetAntigravityTokenTracker(t)
	auth := &cliproxyauth.Auth{ID: "ag-limit-default"}
	if got := drainAntigravityBucket(&config.Config{}, auth); got != 50 {
		t.Fatalf("default burst = %d, want 50", got)
	}
}

func TestAntigravityLocalRateLimitIsConfigurable(t *testing.T) {
	resetAntigravityTokenTracker(t)
	cfg := &config.Config{}
	cfg.Antigravity.LocalRateLimit = config.AntigravityLocalRateLimitConfig{
		MaxTokens: 120, InitialTokens: 120, RegenerationRatePerMinute: 20,
	}
	auth := &cliproxyauth.Auth{ID: "ag-limit-configured"}
	if got := drainAntigravityBucket(cfg, auth); got != 120 {
		t.Fatalf("configured burst = %d, want 120", got)
	}
	if got := antigravity.GetTokenTracker().Config().RegenerationRatePerMinute; got != 20 {
		t.Fatalf("regeneration rate = %v, want 20", got)
	}
	err := antigravityEnsureRequestTokens(cfg, auth, nil)
	var limited antigravityLocalRateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("drained bucket error = %v, want antigravityLocalRateLimitError", err)
	}
	if limited.RetryAfter() == nil || *limited.RetryAfter() <= 0 || *limited.RetryAfter() > 3*time.Second+50*time.Millisecond {
		t.Fatalf("retryAfter = %v, want within (0, 3s]", limited.RetryAfter())
	}
	if got := cliproxyauth.SafeResponseHeaders(err).Get("Retry-After"); got != "3" {
		t.Fatalf("SafeResponseHeaders Retry-After = %q, want 3", got)
	}
	if !cliproxyauth.HasTrustedRetryDeadline(err) {
		t.Fatal("HasTrustedRetryDeadline = false, want true")
	}
}

func TestAntigravityLocalRateLimitCanBeDisabled(t *testing.T) {
	resetAntigravityTokenTracker(t)
	off := false
	cfg := &config.Config{}
	cfg.Antigravity.LocalRateLimit.Enabled = &off
	auth := &cliproxyauth.Auth{ID: "ag-limit-off"}
	for i := 0; i < 1000; i++ {
		if err := antigravityEnsureRequestTokens(cfg, auth, nil); err != nil {
			t.Fatalf("request %d refused with the limiter disabled: %v", i, err)
		}
		antigravityConsumeRequestTokens(cfg, auth, nil)
	}
}

func TestAntigravityLocalRateLimitReloadKeepsBalances(t *testing.T) {
	resetAntigravityTokenTracker(t)
	auth := &cliproxyauth.Auth{ID: "ag-limit-reload"}
	base := &config.Config{}
	for i := 0; i < 10; i++ {
		antigravityConsumeRequestTokens(base, auth, nil)
	}
	raised := &config.Config{}
	raised.Antigravity.LocalRateLimit.RegenerationRatePerMinute = 20
	if err := antigravityEnsureRequestTokens(raised, auth, nil); err != nil {
		t.Fatal(err)
	}
	got := antigravity.GetTokenTracker().GetTokens(antigravityTrackerAccountIndex(auth))
	if got < 39.9 || got > 40.5 {
		t.Fatalf("balance after config reload = %v, want ~40 (unchanged by the reload)", got)
	}
}

func TestAntigravityLocalRateLimitClampsInitialTokensAndMaterializesLowInitial(t *testing.T) {
	resetAntigravityTokenTracker(t)
	cfg := &config.Config{}
	cfg.Antigravity.LocalRateLimit = config.AntigravityLocalRateLimitConfig{
		MaxTokens: 10, InitialTokens: 50, RegenerationRatePerMinute: 600,
	}
	auth := &cliproxyauth.Auth{ID: "ag-limit-clamped"}
	if got := drainAntigravityBucket(cfg, auth); got != 10 {
		t.Fatalf("clamped burst = %d, want 10", got)
	}

	resetAntigravityTokenTracker(t)
	lowInit := &config.Config{}
	lowInit.Antigravity.LocalRateLimit = config.AntigravityLocalRateLimitConfig{
		MaxTokens: 5, InitialTokens: 0.5, RegenerationRatePerMinute: 60000,
	}
	lowAuth := &cliproxyauth.Auth{ID: "ag-limit-low-init"}
	err := antigravityEnsureRequestTokens(lowInit, lowAuth, nil)
	if err == nil {
		t.Fatal("expected first call with 0.5 initial tokens to be refused")
	}
	var limited antigravityLocalRateLimitError
	if !errors.As(err, &limited) || limited.RetryAfter() == nil || *limited.RetryAfter() <= 0 {
		t.Fatalf("expected positive RetryAfter for refillable low-initial bucket, got %v", err)
	}
	tracker := antigravity.GetTokenTracker()
	if gotMax := tracker.GetMaxTokens(); gotMax != 5 {
		t.Fatalf("GetMaxTokens() = %v, want 5", gotMax)
	}
}

func TestAntigravityLocalRateLimitImpossibleCapacityHasNoRetryAfter(t *testing.T) {
	resetAntigravityTokenTracker(t)
	cfg := &config.Config{}
	cfg.Antigravity.LocalRateLimit = config.AntigravityLocalRateLimitConfig{
		MaxTokens: 0.5, InitialTokens: 0.5, RegenerationRatePerMinute: 60,
	}
	err := antigravityEnsureRequestTokens(cfg, &cliproxyauth.Auth{ID: "ag-limit-impossible"}, nil)
	if err == nil {
		t.Fatal("expected refusal when MaxTokens < cost")
	}
	var limited antigravityLocalRateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("error = %T, want antigravityLocalRateLimitError", err)
	}
	if limited.RetryAfter() != nil {
		t.Fatalf("RetryAfter() = %v, want nil when capacity < cost", *limited.RetryAfter())
	}
	if got := cliproxyauth.SafeResponseHeaders(err).Get("Retry-After"); got != "" {
		t.Fatalf("SafeResponseHeaders Retry-After = %q, want empty", got)
	}
}

func TestAntigravityLocalRateLimitStaleExecutorDoesNotRevertReloadedConfig(t *testing.T) {
	resetAntigravityTokenTracker(t)
	oldCfg := &config.Config{}
	oldCfg.Antigravity.LocalRateLimit = config.AntigravityLocalRateLimitConfig{
		MaxTokens: 10, InitialTokens: 10, RegenerationRatePerMinute: 6,
	}
	oldExec := NewAntigravityExecutor(oldCfg)

	newCfg := &config.Config{}
	newCfg.Antigravity.LocalRateLimit = config.AntigravityLocalRateLimitConfig{
		MaxTokens: 80, InitialTokens: 80, RegenerationRatePerMinute: 20,
	}
	_ = NewAntigravityExecutor(newCfg)

	if err := antigravityEnsureRequestTokens(oldExec.cfg, &cliproxyauth.Auth{ID: "ag-stale"}, nil, oldExec.configGen); err != nil {
		t.Fatal(err)
	}
	if got := antigravity.GetTokenTracker().Config().MaxTokens; got != 80 {
		t.Fatalf("stale executor reverted MaxTokens to %v, want 80", got)
	}
}

func TestAntigravityLocalRateLimitErrorIsRequestScoped(t *testing.T) {
	d := 2 * time.Second
	var err error = antigravityLocalRateLimitError{retryAfter: &d}
	var scoped cliproxyexecutor.RequestScopedError
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatal("local rate limit error is not request-scoped")
	}
	var coded interface{ StatusCode() int }
	if !errors.As(err, &coded) || coded.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %v, want 429", coded)
	}
	var hinted interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &hinted) || hinted.RetryAfter() == nil || *hinted.RetryAfter() != 2*time.Second {
		t.Fatal("retry-after hint lost")
	}
}

func TestAntigravityLocalRateLimitEmptySectionKeepsExistingTracker(t *testing.T) {
	resetAntigravityTokenTracker(t)
	antigravity.InitTokenTracker(antigravity.TokenBucketConfig{MaxTokens: 7, InitialTokens: 7, RegenerationRatePerMinute: 1})
	auth := &cliproxyauth.Auth{ID: "ag-limit-empty-section"}
	if got := drainAntigravityBucket(&config.Config{}, auth); got != 7 {
		t.Fatalf("burst with an empty section = %d, want 7 (tracker config preserved)", got)
	}
}

func TestAntigravityLocalRateLimitRemovedSectionRestoresDefaults(t *testing.T) {
	resetAntigravityTokenTracker(t)
	cfg := &config.Config{}
	cfg.Antigravity.LocalRateLimit.RegenerationRatePerMinute = 20
	if err := antigravityEnsureRequestTokens(cfg, &cliproxyauth.Auth{ID: "ag-limit-restore"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := antigravity.GetTokenTracker().Config().RegenerationRatePerMinute; got != 20 {
		t.Fatalf("rate = %v, want 20", got)
	}
	if err := antigravityEnsureRequestTokens(&config.Config{}, &cliproxyauth.Auth{ID: "ag-limit-restore"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := antigravity.GetTokenTracker().Config().RegenerationRatePerMinute; got != 6 {
		t.Fatalf("rate after removing the section = %v, want default 6", got)
	}
}

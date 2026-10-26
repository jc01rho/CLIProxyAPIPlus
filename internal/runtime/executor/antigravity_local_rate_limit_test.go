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
	// 20 per minute is one token every three seconds.
	err := antigravityEnsureRequestTokens(cfg, auth, nil)
	var limited antigravityLocalRateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("drained bucket error = %v, want antigravityLocalRateLimitError", err)
	}
	if limited.retryAfter <= 0 || limited.retryAfter > 3*time.Second+50*time.Millisecond {
		t.Fatalf("retryAfter = %v, want within (0, 3s]", limited.retryAfter)
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

// The refusal must be tagged so the conductor neither cools the credential down nor hands the
// primary role to another account.
func TestAntigravityLocalRateLimitErrorIsRequestScoped(t *testing.T) {
	var err error = antigravityLocalRateLimitError{retryAfter: 2 * time.Second}
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

// An empty local-rate-limit section must not clobber a tracker that was configured elsewhere.
func TestAntigravityLocalRateLimitEmptySectionKeepsExistingTracker(t *testing.T) {
	resetAntigravityTokenTracker(t)
	antigravity.InitTokenTracker(antigravity.TokenBucketConfig{MaxTokens: 7, InitialTokens: 7, RegenerationRatePerMinute: 1})
	auth := &cliproxyauth.Auth{ID: "ag-limit-empty-section"}
	if got := drainAntigravityBucket(&config.Config{}, auth); got != 7 {
		t.Fatalf("burst with an empty section = %d, want 7 (tracker config preserved)", got)
	}
}

// Removing the section after it was applied restores the defaults.
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

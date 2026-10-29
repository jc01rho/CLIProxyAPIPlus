package claudewarmup

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// ErrAlreadyRunning is returned when a warmup run is already in progress.
var ErrAlreadyRunning = errors.New("claude warmup already running")

// runGuard serializes scheduled and manually triggered runs process-wide.
var runGuard sync.Mutex

const (
	providerClaude = "claude"
	warmupPrompt   = "hi"
	retryDelay     = 5 * time.Second
	// fiveHourWindow is the Anthropic unified short window. A cooldown longer than
	// this can only come from the weekly limit.
	fiveHourWindow = 5 * time.Hour
)

// Executor runs one request pinned to a specific auth. *coreauth.Manager satisfies it.
type Executor interface {
	ExecuteWithAuth(ctx context.Context, authID string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

// AuthLister lists known auths. *coreauth.Manager satisfies it.
type AuthLister interface {
	List() []*coreauth.Auth
}

// Manager is the subset of *coreauth.Manager the warmup needs.
type Manager interface {
	AuthLister
	Executor
}

// AccountResult is the outcome for one credential.
type AccountResult struct {
	AuthID     string `json:"auth_id"`
	Label      string `json:"label,omitempty"`
	Success    bool   `json:"success"`
	Attempts   int    `json:"attempts"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Options tune one run. Zero values use production behavior.
type Options struct {
	// NoJitter skips the random delay between accounts.
	NoJitter bool
	// Now overrides the clock used for eligibility checks.
	Now func() time.Time
	// Sleep overrides the context-aware wait used for jitter and retry delays.
	Sleep func(ctx context.Context, d time.Duration) error
	// RandN returns a value in [0, n). It drives jitter.
	RandN func(n int64) int64
}

// Eligible returns Claude OAuth auths that are enabled and not blocked by the
// weekly limit, sorted by ID for a stable order.
func Eligible(auths []*coreauth.Auth, now time.Time) []*coreauth.Auth {
	out := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), providerClaude) {
			continue
		}
		if auth.AuthKind() != coreauth.AuthKindOAuth {
			continue
		}
		if auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}
		if blockedByWeeklyLimit(auth, now) {
			continue
		}
		out = append(out, auth)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// blockedByWeeklyLimit reports whether the last observed Anthropic headers or
// the current cooldown show the credential is locked out by the 7-day limit.
func blockedByWeeklyLimit(auth *coreauth.Auth, now time.Time) bool {
	signals := auth.Quota.Signals
	if strings.EqualFold(strings.TrimSpace(signals["Anthropic-Ratelimit-Unified-7d-Status"]), "rejected") {
		reset := unixSeconds(signals["Anthropic-Ratelimit-Unified-7d-Reset"])
		if reset.IsZero() || reset.After(now) {
			return true
		}
	}
	if auth.Quota.Exceeded && auth.Quota.NextRecoverAt.Sub(now) > fiveHourWindow {
		return true
	}
	return false
}

func unixSeconds(raw string) time.Time {
	secs, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}

// BuildPayload returns the minimal Claude Messages request body.
func BuildPayload(model string, maxTokens int) []byte {
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []map[string]any{{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": warmupPrompt}},
		}},
	})
	return body
}

// Run sends one warmup request to every eligible Claude OAuth auth, retrying a
// failed account once. It returns ErrAlreadyRunning when another run is active.
func Run(ctx context.Context, mgr Manager, cfg config.ClaudeWarmupConfig, opts Options) ([]AccountResult, error) {
	if mgr == nil {
		return nil, errors.New("claude warmup: auth manager unavailable")
	}
	if !runGuard.TryLock() {
		return nil, ErrAlreadyRunning
	}
	defer runGuard.Unlock()

	cfg = cfg.WithDefaults()
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	sleep := sleepContext
	if opts.Sleep != nil {
		sleep = opts.Sleep
	}
	randN := rand.Int64N
	if opts.RandN != nil {
		randN = opts.RandN
	}
	jitter := cfg.JitterDuration()
	if opts.NoJitter {
		jitter = 0
	}

	auths := Eligible(mgr.List(), now())
	log.Infof("claude warmup: starting run for %d eligible account(s) (model=%s, max_tokens=%d)", len(auths), cfg.Model, cfg.MaxTokens)
	payload := BuildPayload(cfg.Model, cfg.MaxTokens)
	results := make([]AccountResult, 0, len(auths))
	for i, auth := range auths {
		if i > 0 && jitter > 0 {
			if errSleep := sleep(ctx, time.Duration(randN(int64(jitter)))); errSleep != nil {
				return results, errSleep
			}
		}
		result := warmOne(ctx, mgr, auth, cfg.Model, payload, sleep)
		results = append(results, result)
		if result.Success {
			log.Infof("claude warmup: auth=%s label=%s ok (attempts=%d)", auth.ID, auth.Label, result.Attempts)
		} else {
			log.Infof("claude warmup: auth=%s label=%s failed (attempts=%d, status=%d): %s", auth.ID, auth.Label, result.Attempts, result.HTTPStatus, result.Error)
		}
		if errCtx := ctx.Err(); errCtx != nil {
			return results, errCtx
		}
	}
	return results, nil
}

func warmOne(ctx context.Context, mgr Executor, auth *coreauth.Auth, model string, payload []byte, sleep func(context.Context, time.Duration) error) AccountResult {
	result := AccountResult{AuthID: auth.ID, Label: auth.Label}
	req := cliproxyexecutor.Request{Model: model, Payload: payload, Format: sdktranslator.FormatClaude}
	execOpts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}
	for attempt := 1; attempt <= 2; attempt++ {
		result.Attempts = attempt
		_, err := mgr.ExecuteWithAuth(ctx, auth.ID, req, execOpts)
		if err == nil {
			result.Success, result.HTTPStatus, result.Error = true, 0, ""
			return result
		}
		result.HTTPStatus = statusCode(err)
		result.Error = truncate(err.Error(), 300)
		// A 429 means the window is already running or the account is locked;
		// retrying cannot help and only adds traffic.
		if attempt == 2 || result.HTTPStatus == http.StatusTooManyRequests || ctx.Err() != nil {
			return result
		}
		if errSleep := sleep(ctx, retryDelay); errSleep != nil {
			return result
		}
	}
	return result
}

func statusCode(err error) int {
	var withStatus interface{ StatusCode() int }
	if errors.As(err, &withStatus) && withStatus != nil {
		return withStatus.StatusCode()
	}
	var authErr *coreauth.Error
	if errors.As(err, &authErr) && authErr != nil {
		return authErr.HTTPStatus
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

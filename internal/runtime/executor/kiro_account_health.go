package executor

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const (
	kiroDefaultMaxInflight    = 4
	kiroHealthRefreshInterval = 5 * time.Minute
	kiroBusyRetryAfter        = time.Second
)

type kiroQuotaObservation struct {
	remaining float64
	expiresAt time.Time
}

type kiroAccountHealthState struct {
	consecutiveFailures int
	circuitUntil        time.Time
	inflight            int
	quota               kiroQuotaObservation
	nextUsageCheck      time.Time
}

// Ported from kiro-lb src/pool.rs (1581af9). Routing hints are process-local,
// keyed by auth ID, and never modify the conductor or selector.
type kiroAccountHealthRegistry struct {
	mu    sync.Mutex
	clock func() time.Time
	rng   func() float64
	byID  map[string]*kiroAccountHealthState
}

var defaultKiroAccountHealth = newKiroAccountHealthRegistry(time.Now, rand.Float64)

func newKiroAccountHealthRegistry(clock func() time.Time, rng func() float64) *kiroAccountHealthRegistry {
	return &kiroAccountHealthRegistry{clock: clock, rng: rng, byID: make(map[string]*kiroAccountHealthState)}
}

func (e *KiroExecutor) accountHealth() *kiroAccountHealthRegistry {
	if e.health != nil {
		return e.health
	}
	return defaultKiroAccountHealth
}

func (h *kiroAccountHealthRegistry) stateLocked(id string) *kiroAccountHealthState {
	state := h.byID[id]
	if state == nil {
		state = &kiroAccountHealthState{}
		h.byID[id] = state
	}
	return state
}

func (h *kiroAccountHealthRegistry) admit(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	state := h.stateLocked(id)
	if state.circuitUntil.After(now) && h.rng() >= 0.10 {
		return kiroHealthRetryError(http.StatusServiceUnavailable, "kiro local circuit open", state.circuitUntil.Sub(now))
	}
	if !state.quota.expiresAt.After(now) || state.quota.remaining > 0 || h.allKnownAccountsDepletedLocked(now) {
		return nil
	}
	return kiroHealthRetryError(http.StatusTooManyRequests, "kiro local quota depleted", state.quota.expiresAt.Sub(now))
}

func (h *kiroAccountHealthRegistry) allKnownAccountsDepletedLocked(now time.Time) bool {
	for _, state := range h.byID {
		// Unknown or expired observations are not evidence of depletion.
		if !state.quota.expiresAt.After(now) || state.quota.remaining > 0 {
			return false
		}
	}
	return len(h.byID) > 0
}

func (h *kiroAccountHealthRegistry) observeQuota(id string, remaining float64, resetAt time.Time, interval time.Duration) {
	if math.IsNaN(remaining) || math.IsInf(remaining, 0) {
		return
	}
	if interval <= 0 {
		interval = kiroHealthRefreshInterval
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	expiresAt := now.Add(2 * interval)
	if !resetAt.IsZero() && resetAt.Before(expiresAt) {
		expiresAt = resetAt
	}
	h.stateLocked(id).quota = kiroQuotaObservation{remaining: remaining, expiresAt: expiresAt}
}

func (h *kiroAccountHealthRegistry) success(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.stateLocked(id)
	state.consecutiveFailures = 0
	state.circuitUntil = time.Time{}
}

func kiroHealthRecoverable(status int, body []byte) bool {
	// These have dedicated routing treatments, not account circuit failures.
	switch kiroErrorReasonFromBody(body) {
	case "USER_REQUEST_RATE_EXCEEDED", "INVALID_MODEL_ID", "MONTHLY_REQUEST_COUNT":
		return false
	}
	if strings.HasPrefix(string(body), "kiro local ") || isKiroSuspendedBody(body) {
		return false
	}
	return classifyKiroUpstreamError(status, body) == KiroErrorRecoverable
}

func (h *kiroAccountHealthRegistry) failure(id string, status int, body []byte) {
	if !kiroHealthRecoverable(status, body) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.stateLocked(id)
	// Saturating the counter avoids overflow, while 2^11 already exceeds 1440.
	if state.consecutiveFailures < 12 {
		state.consecutiveFailures++
	}
	multiplier := min(1<<(state.consecutiveFailures-1), 1440)
	state.circuitUntil = h.clock().Add(time.Minute * time.Duration(multiplier))
}

func (h *kiroAccountHealthRegistry) result(id string, ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	if err == nil {
		h.success(id)
		return nil
	}
	code, body := kiroErrorStatus(err)
	var netErr net.Error
	var streamErr *EventStreamError
	if code == 0 && (errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF) ||
		(errors.As(err, &streamErr) && (errors.As(streamErr.Cause, &netErr) || errors.Is(streamErr.Cause, io.ErrUnexpectedEOF)))) {
		code = http.StatusBadGateway
	}
	if !kiroHealthRecoverable(code, body) {
		return err
	}
	h.failure(id, code, body)
	h.mu.Lock()
	delay := h.byID[id].circuitUntil.Sub(h.clock())
	h.mu.Unlock()
	var ra interface{ RetryAfter() *time.Duration }
	if errors.As(err, &ra) && ra.RetryAfter() != nil && *ra.RetryAfter() > delay {
		delay = *ra.RetryAfter()
	}
	return kiroHealthRetryError(code, err.Error(), delay)
}

func kiroHealthRetryError(code int, message string, delay time.Duration) statusErr {
	return statusErr{code: code, msg: message, retryAfter: &delay, credentialScoped: true}
}

// Ported from kiro-lb src/upstream/http.rs (1581af9): do not queue a saturated
// account. The existing credential-scoped retry contract selects another one.
func (h *kiroAccountHealthRegistry) acquire(id string, limit int) (func(), error) {
	if limit <= 0 {
		limit = kiroDefaultMaxInflight
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.stateLocked(id)
	if state.inflight >= limit {
		return nil, kiroHealthRetryError(http.StatusTooManyRequests, "kiro local account busy", kiroBusyRetryAfter)
	}
	state.inflight++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			state.inflight--
			h.mu.Unlock()
		})
	}, nil
}

func wrapKiroHealthStream(ctx context.Context, in <-chan cliproxyexecutor.StreamChunk, release func(), result func(error) error, cancel context.CancelFunc) <-chan cliproxyexecutor.StreamChunk {
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer release()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-in:
				if !ok {
					result(nil)
					return
				}
				if chunk.Err != nil {
					chunk.Err = result(chunk.Err)
					// Release before handing the terminal error to a slow client.
					release()
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
				if chunk.Err != nil {
					return
				}
			}
		}
	}()
	return out
}

type kiroQuotaContextKey struct{}
type kiroQuotaRecorder struct {
	health *kiroAccountHealthRegistry
	id     string
}

func consumeKiroQuota(ctx context.Context, credits float64) {
	recorder, ok := ctx.Value(kiroQuotaContextKey{}).(kiroQuotaRecorder)
	if !ok || credits <= 0 {
		return
	}
	h := recorder.health
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.stateLocked(recorder.id)
	if state.quota.expiresAt.After(h.clock()) {
		state.quota.remaining -= credits
	}
}

// Usage observations are refreshed at most once per interval per account. A
// failed usage API never blocks generation and never fabricates zero headroom.
func (e *KiroExecutor) refreshKiroQuota(ctx context.Context, auth *cliproxyauth.Auth) {
	h := e.accountHealth()
	id := kiroHealthAccountKey(auth)
	h.mu.Lock()
	state := h.stateLocked(id)
	if state.nextUsageCheck.After(h.clock()) {
		h.mu.Unlock()
		return
	}
	state.nextUsageCheck = h.clock().Add(kiroHealthRefreshInterval)
	h.mu.Unlock()
	localAuth, err := e.withKiroMachineID(auth)
	if err != nil {
		log.Debugf("kiro: quota fingerprint unavailable: %v", err)
		return
	}
	auth = localAuth
	access, arn := kiroCredentials(auth)
	checker := kiroauth.NewUsageCheckerWithClient(newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 30*time.Second))
	status, err := checker.GetQuotaStatus(kiroauth.WithMachineID(ctx, ensureKiroMachineID(auth)), &kiroauth.KiroTokenData{
		AccessToken: access, ProfileArn: arn, APIRegion: resolveKiroAPIRegion(auth),
		AuthMethod: getAuthValue(auth, "auth_method"),
	})
	if err != nil {
		log.Debugf("kiro: quota observation unavailable: %v", err)
		return
	}
	h.observeQuota(id, status.RemainingQuota, status.NextReset, kiroHealthRefreshInterval)
}

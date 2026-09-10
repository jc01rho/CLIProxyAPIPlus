package executor

import (
	"context"
	"testing"
	"time"
)

// TestZcodeSigningGateCacheIsPerCredential verifies the feature-gate answer is
// cached per credential (extension parity: per-API-key states map) and a cold
// credential fails closed without hanging.
func TestZcodeSigningGateCacheIsPerCredential(t *testing.T) {
	zcodeResetSigningStateForTests()
	defer zcodeResetSigningStateForTests()

	zcodeSigningMu.Lock()
	zcodeGateStates["warm-credential"] = zcodeSignGate{enabled: true, checkedAt: time.Now()}
	zcodeSigningMu.Unlock()

	if !zcodeSigningEnabled(context.Background(), "warm-credential") {
		t.Error("a cached affirmative gate answer must be reused")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if zcodeSigningEnabled(cancelled, "cold-credential") {
		t.Error("a cold credential with a cancelled context must read as disabled")
	}
	if !zcodeSigningEnabled(cancelled, "warm-credential") {
		t.Error("another credential's negative probe must not evict the cached answer")
	}
	zcodeSigningMu.Lock()
	_, cached := zcodeGateStates["cold-credential"]
	zcodeSigningMu.Unlock()
	if cached {
		t.Error("negative gate answers must not be cached")
	}
}

// TestZcodeSigningGateCacheExpires verifies the one-hour TTL is enforced.
func TestZcodeSigningGateCacheExpires(t *testing.T) {
	zcodeResetSigningStateForTests()
	defer zcodeResetSigningStateForTests()

	zcodeSigningMu.Lock()
	zcodeGateStates["stale-credential"] = zcodeSignGate{enabled: true, checkedAt: time.Now().Add(-2 * zcodeGateTTL)}
	zcodeSigningMu.Unlock()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if zcodeSigningEnabled(cancelled, "stale-credential") {
		t.Error("a stale gate answer must be re-probed, not reused")
	}
}

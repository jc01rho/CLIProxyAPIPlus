package executor

// Endpoint rotation state for the Kiro generation executor.
//
// Ported behavior from kiro-lb's kiro/endpoints.py (AGPL-3.0, minpeter/jc01rho
// fork of jwadow/kiro-gateway): per-(account,model) affinity plus per-endpoint
// cooldown, with cooling endpoints moved to the back of the attempt order but
// never removed. State is per-process and in memory; a restart falls back to
// the declared order (see buildKiroEndpointConfigsForAuth).

import (
	"sync"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// kiroEndpointRotationState holds the in-memory affinity and cooldown maps
// used to compute per-request endpoint attempt order. Guarded by mu.
type kiroEndpointRotationState struct {
	mu            sync.Mutex
	affinity      map[string]string    // (authID|model) -> endpoint key
	cooldownUntil map[string]time.Time // endpoint key -> cooldown expiry
}

var kiroEndpointRotation = &kiroEndpointRotationState{
	affinity:      make(map[string]string),
	cooldownUntil: make(map[string]time.Time),
}

// kiroEndpointAffinityKey builds the (account, model) affinity map key.
// getAccountKey(auth) already resolves a stable per-account identity
// (client_id+refresh_token, then auth.ID, then profile_arn, then access
// token, then a fixed anonymous seed), so it is reused as-is here.
func kiroEndpointAffinityKey(auth *cliproxyauth.Auth, model string) string {
	return getAccountKey(auth) + "|" + model
}

// kiroRecordEndpointSuccess records that endpointKey served (auth, model)
// successfully: it becomes the preferred endpoint for that pair, and any
// cooldown on it is cleared immediately (mirrors kiro-lb record_success).
func kiroRecordEndpointSuccess(auth *cliproxyauth.Auth, model, endpointKey string) {
	if endpointKey == "" {
		return
	}
	key := kiroEndpointAffinityKey(auth, model)
	kiroEndpointRotation.mu.Lock()
	defer kiroEndpointRotation.mu.Unlock()
	kiroEndpointRotation.affinity[key] = endpointKey
	delete(kiroEndpointRotation.cooldownUntil, endpointKey)
}

// kiroRecordEndpointFailure puts endpointKey into cooldown for the given
// duration (mirrors kiro-lb record_failure). A non-positive duration is a
// no-op: nothing to cool down.
func kiroRecordEndpointFailure(endpointKey string, cooldown time.Duration) {
	if endpointKey == "" || cooldown <= 0 {
		return
	}
	kiroEndpointRotation.mu.Lock()
	defer kiroEndpointRotation.mu.Unlock()
	kiroEndpointRotation.cooldownUntil[endpointKey] = time.Now().Add(cooldown)
}

// kiroEndpointIsCoolingLocked reports whether endpointKey is currently in
// cooldown. An expired entry is lazily evicted (mirrors kiro-lb is_cooling).
// Caller must hold kiroEndpointRotation.mu.
func kiroEndpointIsCoolingLocked(endpointKey string, now time.Time) bool {
	until, ok := kiroEndpointRotation.cooldownUntil[endpointKey]
	if !ok {
		return false
	}
	if !until.After(now) {
		delete(kiroEndpointRotation.cooldownUntil, endpointKey)
		return false
	}
	return true
}

// kiroEndpointAttemptOrder returns configs reordered for this (auth, model)
// request: affinity-first (if the preferred endpoint is present and not
// cooling), then the declared order, with cooling endpoints moved to the
// back. Nothing is ever removed from the returned slice - a cooldown must
// not turn a request into a hard failure (mirrors kiro-lb attempt_order).
// An affinity key pointing at an endpoint absent from configs is ignored.
func kiroEndpointAttemptOrder(configs []kiroEndpointConfig, auth *cliproxyauth.Auth, model string) []kiroEndpointConfig {
	if len(configs) == 0 {
		return configs
	}

	key := kiroEndpointAffinityKey(auth, model)

	kiroEndpointRotation.mu.Lock()
	defer kiroEndpointRotation.mu.Unlock()

	now := time.Now()
	preferredKey := kiroEndpointRotation.affinity[key]

	ranked := make([]kiroEndpointConfig, 0, len(configs))
	placed := make(map[string]bool, len(configs))

	if preferredKey != "" {
		for _, c := range configs {
			if c.Key == preferredKey && !kiroEndpointIsCoolingLocked(c.Key, now) {
				ranked = append(ranked, c)
				placed[c.Key] = true
				break
			}
		}
	}

	var ready, cooling []kiroEndpointConfig
	for _, c := range configs {
		if placed[c.Key] {
			continue
		}
		if kiroEndpointIsCoolingLocked(c.Key, now) {
			cooling = append(cooling, c)
		} else {
			ready = append(ready, c)
		}
	}

	ranked = append(ranked, ready...)
	ranked = append(ranked, cooling...)
	return ranked
}

// kiroResetEndpointRotationState clears affinity and cooldown state. Used by
// tests to guarantee deterministic starting conditions.
func kiroResetEndpointRotationState() {
	kiroEndpointRotation.mu.Lock()
	defer kiroEndpointRotation.mu.Unlock()
	kiroEndpointRotation.affinity = make(map[string]string)
	kiroEndpointRotation.cooldownUntil = make(map[string]time.Time)
}

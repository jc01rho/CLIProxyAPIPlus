package executor

// Tests for Kiro generation endpoint rotation.
//
// Ported behavior from kiro-lb's kiro/endpoints.py (AGPL-3.0, minpeter/jc01rho
// fork of jwadow/kiro-gateway). Deterministic: cooldown/affinity state is
// manipulated directly instead of sleeping on the wall clock.

import (
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func kiroTestEndpointConfigs() []kiroEndpointConfig {
	return []kiroEndpointConfig{
		{Key: "runtime", Name: "KiroRuntime"},
		{Key: "codewhisperer", Name: "CodeWhisperer"},
		{Key: "amazonq", Name: "AmazonQ"},
	}
}

func kiroTestKeys(configs []kiroEndpointConfig) []string {
	keys := make([]string, len(configs))
	for i, c := range configs {
		keys[i] = c.Key
	}
	return keys
}

func assertKiroKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("attempt order length = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempt order = %v, want %v", got, want)
		}
	}
}

func TestKiroEndpointAttemptOrder_NoStateUsesDeclaredOrder(t *testing.T) {
	kiroResetEndpointRotationState()
	auth := &cliproxyauth.Auth{ID: "auth-1"}

	order := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), auth, "claude-sonnet-4.6")

	assertKiroKeys(t, kiroTestKeys(order), "runtime", "codewhisperer", "amazonq")
}

func TestKiroEndpointAttemptOrder_AffinityMovesPreferredFirst(t *testing.T) {
	kiroResetEndpointRotationState()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	model := "claude-sonnet-4.6"

	kiroRecordEndpointSuccess(auth, model, "codewhisperer")

	order := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), auth, model)

	assertKiroKeys(t, kiroTestKeys(order), "codewhisperer", "runtime", "amazonq")
}

func TestKiroEndpointAttemptOrder_CoolingEndpointMovesToBackButStays(t *testing.T) {
	kiroResetEndpointRotationState()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	model := "claude-sonnet-4.6"

	// Simulate a very recent 429 on the primary endpoint: it must be
	// deprioritized, never dropped from the returned slice.
	kiroRecordEndpointFailure("runtime", time.Minute)

	order := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), auth, model)

	assertKiroKeys(t, kiroTestKeys(order), "codewhisperer", "amazonq", "runtime")
}

func TestKiroEndpointAttemptOrder_ExpiredCooldownIsTreatedAsReady(t *testing.T) {
	kiroResetEndpointRotationState()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	model := "claude-sonnet-4.6"

	// Manipulate the cooldown timestamp directly to simulate expiry instead
	// of sleeping on the wall clock.
	kiroEndpointRotation.mu.Lock()
	kiroEndpointRotation.cooldownUntil["runtime"] = time.Now().Add(-time.Second)
	kiroEndpointRotation.mu.Unlock()

	order := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), auth, model)

	assertKiroKeys(t, kiroTestKeys(order), "runtime", "codewhisperer", "amazonq")
}

func TestKiroEndpointAttemptOrder_SuccessClearsCooldownAndSetsAffinity(t *testing.T) {
	kiroResetEndpointRotationState()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	model := "claude-sonnet-4.6"

	kiroRecordEndpointFailure("amazonq", time.Minute)
	kiroRecordEndpointSuccess(auth, model, "amazonq")

	// Cooldown cleared: amazonq must now be ready, not cooling.
	kiroEndpointRotation.mu.Lock()
	_, stillCooling := kiroEndpointRotation.cooldownUntil["amazonq"]
	kiroEndpointRotation.mu.Unlock()
	if stillCooling {
		t.Fatalf("expected cooldown to be cleared after success, but it is still set")
	}

	// Affinity set: amazonq must now be preferred first.
	order := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), auth, model)
	assertKiroKeys(t, kiroTestKeys(order), "amazonq", "runtime", "codewhisperer")
}

func TestKiroEndpointAttemptOrder_PreferredEndpointStillCoolingFallsBackToDeclaredOrder(t *testing.T) {
	kiroResetEndpointRotationState()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	model := "claude-sonnet-4.6"

	kiroRecordEndpointSuccess(auth, model, "runtime")
	// A later failure re-cools the previously preferred endpoint.
	kiroRecordEndpointFailure("runtime", time.Minute)

	order := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), auth, model)

	// runtime is still the affinity target but is cooling, so it must not be
	// ranked first; it falls into the cooling tail instead.
	assertKiroKeys(t, kiroTestKeys(order), "codewhisperer", "amazonq", "runtime")
}

func TestKiroEndpointAttemptOrder_UnknownAffinityKeyIsIgnored(t *testing.T) {
	kiroResetEndpointRotationState()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	model := "claude-sonnet-4.6"

	key := kiroEndpointAffinityKey(auth, model)
	kiroEndpointRotation.mu.Lock()
	kiroEndpointRotation.affinity[key] = "not-a-real-endpoint"
	kiroEndpointRotation.mu.Unlock()

	order := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), auth, model)

	assertKiroKeys(t, kiroTestKeys(order), "runtime", "codewhisperer", "amazonq")
}

func TestKiroEndpointAttemptOrder_AffinityIsPerAccountAndModel(t *testing.T) {
	kiroResetEndpointRotationState()
	authA := &cliproxyauth.Auth{ID: "auth-a"}
	authB := &cliproxyauth.Auth{ID: "auth-b"}
	model := "claude-sonnet-4.6"

	kiroRecordEndpointSuccess(authA, model, "amazonq")

	orderA := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), authA, model)
	orderB := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), authB, model)
	orderAOtherModel := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), authA, "other-model")

	assertKiroKeys(t, kiroTestKeys(orderA), "amazonq", "runtime", "codewhisperer")
	assertKiroKeys(t, kiroTestKeys(orderB), "runtime", "codewhisperer", "amazonq")
	assertKiroKeys(t, kiroTestKeys(orderAOtherModel), "runtime", "codewhisperer", "amazonq")
}

func TestKiroEndpointAttemptOrder_NeverRemovesAnEndpoint(t *testing.T) {
	kiroResetEndpointRotationState()
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	model := "claude-sonnet-4.6"

	kiroRecordEndpointFailure("runtime", time.Minute)
	kiroRecordEndpointFailure("codewhisperer", time.Minute)
	kiroRecordEndpointFailure("amazonq", time.Minute)

	order := kiroEndpointAttemptOrder(kiroTestEndpointConfigs(), auth, model)

	if len(order) != 3 {
		t.Fatalf("expected all 3 endpoints to remain present even while cooling, got %d: %v", len(order), kiroTestKeys(order))
	}
}

func TestKiroEndpointRecordFailure_NonPositiveCooldownIsNoop(t *testing.T) {
	kiroResetEndpointRotationState()

	kiroRecordEndpointFailure("runtime", 0)
	kiroRecordEndpointFailure("runtime", -time.Second)

	kiroEndpointRotation.mu.Lock()
	_, cooling := kiroEndpointRotation.cooldownUntil["runtime"]
	kiroEndpointRotation.mu.Unlock()
	if cooling {
		t.Fatalf("expected no cooldown to be recorded for a non-positive duration")
	}
}

func TestKiroEndpointConfigsForAuth_ReturnsThreeEndpointsInDeclaredOrder(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-1"}

	configs := buildKiroEndpointConfigsForAuth(auth)

	assertKiroKeys(t, kiroTestKeys(configs), "runtime", "codewhisperer", "amazonq")

	for _, c := range configs {
		if c.URL == "" {
			t.Fatalf("endpoint %q has empty URL", c.Key)
		}
		if c.Origin == "" {
			t.Fatalf("endpoint %q has empty Origin", c.Key)
		}
		if c.AmzTarget == "" {
			t.Fatalf("endpoint %q has empty AmzTarget", c.Key)
		}
	}
}

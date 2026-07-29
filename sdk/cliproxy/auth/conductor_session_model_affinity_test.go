package auth

import (
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManager_CloseExecutionSession_reclaims_model_affinity_when_execution_session_closed(t *testing.T) {
	// Given
	manager := NewManager(nil, nil, nil)
	routeModel := "alias-model"
	targetKeys := sessionModelAffinityKeys(routeModel, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "closed-session",
		},
	})
	otherKeys := sessionModelAffinityKeys(routeModel, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "kept-session",
		},
	})
	manager.rememberSessionModelAffinityForKeys(targetKeys, "upstream-a", true)
	manager.rememberSessionModelAffinityForKeys(otherKeys, "upstream-b", true)

	// When
	manager.CloseExecutionSession("closed-session")

	// Then
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	for _, key := range targetKeys {
		if _, ok := manager.sessionModelBindings[key]; ok {
			t.Fatalf("sessionModelBindings[%q] still exists after CloseExecutionSession", key)
		}
	}
	for _, key := range otherKeys {
		if _, ok := manager.sessionModelBindings[key]; !ok {
			t.Fatalf("sessionModelBindings[%q] was removed for a different execution session", key)
		}
	}
}

func TestManager_ApplySessionModelAffinity_prunes_expired_bindings_when_accessing_different_session(t *testing.T) {
	// Given
	manager := NewManager(nil, nil, nil)
	now := time.Now()
	manager.mu.Lock()
	manager.sessionModelBindings["exec:expired-session::alias-model"] = sessionModelBinding{
		upstreamModel: "upstream-a",
		expiresAt:     now.Add(-time.Minute),
	}
	manager.sessionModelBindings["exec:active-session::alias-model"] = sessionModelBinding{
		upstreamModel: "upstream-b",
		expiresAt:     now.Add(time.Minute),
	}
	manager.mu.Unlock()

	// When
	manager.applySessionModelAffinityForKeys(
		[]string{"exec:new-session::alias-model"},
		[]string{"upstream-a", "upstream-b"},
	)

	// Then
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if _, ok := manager.sessionModelBindings["exec:expired-session::alias-model"]; ok {
		t.Fatal("expired session model binding was not pruned")
	}
	if _, ok := manager.sessionModelBindings["exec:active-session::alias-model"]; !ok {
		t.Fatal("active session model binding was pruned")
	}
}

package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func newClaudeReloadFixture(t *testing.T, fileToken string) (*Manager, *unauthorizedRefreshExecutor, string, string) {
	t.Helper()
	model := "claude-opus-5"
	path := filepath.Join(t.TempDir(), "claude-reload.json")
	data, errMarshal := json.Marshal(map[string]any{"type": "claude", "access_token": fileToken, "refresh_token": "rt-from-file"})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	primary := &Auth{
		ID:         "claude-reload",
		Provider:   "claude",
		Attributes: map[string]string{AttributePath: path},
		Metadata:   map[string]any{"access_token": "stale-claude-token", "refresh_token": "rt-in-memory"},
	}
	executor := &unauthorizedRefreshExecutor{
		id:            "claude",
		tokenInvalid:  map[string]struct{}{"stale-claude-token": {}},
		refreshTokens: map[string]string{primary.ID: "refreshed-claude-token"},
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(primary.ID) })
	if _, errRegister := m.Register(context.Background(), primary); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	return m, executor, primary.ID, model
}

// A 401 on a Claude credential whose file already holds a newer access token
// (another process refreshed it) retries with that token and does not spend
// the refresh token again.
func TestManager_Execute_ClaudeUnauthorizedAdoptsNewerStoredToken(t *testing.T) {
	m, executor, id, model := newClaudeReloadFixture(t, "fresh-claude-token-from-file")
	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if got := string(resp.Payload); got != id+":fresh-claude-token-from-file" {
		t.Fatalf("payload = %q, want retry with the stored token", got)
	}
	if got := executor.RefreshCalls(); got != 0 {
		t.Fatalf("Refresh calls = %d, want 0 when the store already holds a newer token", got)
	}
	updated, _ := m.GetByID(id)
	if got := authMetadataString(updated, "refresh_token"); got != "rt-from-file" {
		t.Fatalf("refresh_token = %q, want the stored one", got)
	}
}

// When the file still holds the rejected token, the usual refresh runs.
func TestManager_Execute_ClaudeUnauthorizedRefreshesWhenStoreIsStale(t *testing.T) {
	m, executor, id, model := newClaudeReloadFixture(t, "stale-claude-token")
	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if got := string(resp.Payload); got != id+":refreshed-claude-token" {
		t.Fatalf("payload = %q, want refreshed token", got)
	}
	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("Refresh calls = %d, want 1", got)
	}
}

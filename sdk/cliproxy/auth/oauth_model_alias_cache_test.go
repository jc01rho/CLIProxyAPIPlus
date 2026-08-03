package auth

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// loadOAuthAliasTableForTest returns the active immutable alias table snapshot
// stored on the manager. Tests use it to observe the table-local compute
// counter that backs the snapshot memoization.
func loadOAuthAliasTableForTest(t *testing.T, m *Manager) *oauthModelAliasTable {
	t.Helper()
	raw := m.oauthModelAlias.Load()
	table, ok := raw.(*oauthModelAliasTable)
	if !ok || table == nil {
		t.Fatalf("oauth model alias table not loaded: %T", raw)
	}
	return table
}

func newAliasTestManager(t *testing.T, aliases map[string][]internalconfig.OAuthModelAlias) *Manager {
	t.Helper()
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)
	return mgr
}

// TestOAuthAliasCache_PositiveLookupReused verifies that repeated positive (hit)
// lookups against the same snapshot and key reuse the memoized table-only
// resolution instead of recomputing it.
func TestOAuthAliasCache_PositiveLookupReused(t *testing.T) {
	t.Parallel()

	mgr := newAliasTestManager(t, map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5-real", Alias: "gpt-5-alias"}},
	})
	auth := &Auth{ID: "alias-cache-pos", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}

	first := mgr.resolveOAuthModelAliasWithResult(auth, "gpt-5-alias")
	if first.UpstreamModel != "gpt-5-real" {
		t.Fatalf("first lookup upstream = %q, want gpt-5-real", first.UpstreamModel)
	}

	table := loadOAuthAliasTableForTest(t, mgr)
	afterFirst := table.tableOnlyComputeCount()
	if afterFirst != 1 {
		t.Fatalf("compute count after first positive lookup = %d, want 1", afterFirst)
	}

	// Repeated lookups (including via the string-returning wrapper) must be served
	// from the memo and must NOT trigger another table-only computation.
	second := mgr.resolveOAuthModelAliasWithResult(auth, "gpt-5-alias")
	if second != first {
		t.Fatalf("second lookup = %+v, want identical %+v", second, first)
	}
	wrapped := mgr.applyOAuthModelAlias(auth, "gpt-5-alias")
	if wrapped != first.UpstreamModel {
		t.Fatalf("applyOAuthModelAlias = %q, want %q", wrapped, first.UpstreamModel)
	}
	if got := table.tableOnlyComputeCount(); got != afterFirst {
		t.Fatalf("compute count after repeated positive lookups = %d, want %d (memo reuse)", got, afterFirst)
	}
}

// TestOAuthAliasCache_NegativeLookupReused verifies that negative (miss) lookups
// are also cached safely so the table-only resolution is not recomputed.
func TestOAuthAliasCache_NegativeLookupReused(t *testing.T) {
	t.Parallel()

	mgr := newAliasTestManager(t, map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5-real", Alias: "gpt-5-alias"}},
	})
	auth := &Auth{ID: "alias-cache-neg", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}

	first := mgr.resolveOAuthModelAliasWithResult(auth, "totally-unknown-model")
	if first.UpstreamModel != "" {
		t.Fatalf("negative lookup upstream = %q, want empty", first.UpstreamModel)
	}

	table := loadOAuthAliasTableForTest(t, mgr)
	afterFirst := table.tableOnlyComputeCount()
	if afterFirst != 1 {
		t.Fatalf("compute count after first negative lookup = %d, want 1", afterFirst)
	}

	second := mgr.resolveOAuthModelAliasWithResult(auth, "totally-unknown-model")
	if second.UpstreamModel != "" {
		t.Fatalf("repeated negative lookup upstream = %q, want empty", second.UpstreamModel)
	}
	if got := table.tableOnlyComputeCount(); got != afterFirst {
		t.Fatalf("compute count after repeated negative lookup = %d, want %d (memo reuse)", got, afterFirst)
	}
}

// TestOAuthAliasCache_UpstreamDirectPreserved verifies Phase 1: when the requested
// model is itself a configured upstream model name, it is preserved as-is via the
// precomputed per-channel upstream-name set (O(1) lookup), including any suffix.
func TestOAuthAliasCache_UpstreamDirectPreserved(t *testing.T) {
	t.Parallel()

	mgr := newAliasTestManager(t, map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-real-upstream", Alias: "pretty-alias"}},
	})
	auth := &Auth{ID: "alias-cache-direct", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}

	// Requesting the upstream model name itself must be preserved as-is.
	if got := mgr.resolveOAuthModelAliasWithResult(auth, "gpt-real-upstream").UpstreamModel; got != "gpt-real-upstream" {
		t.Fatalf("upstream-direct upstream = %q, want gpt-real-upstream", got)
	}
	// Case-insensitive upstream-name match must also be preserved.
	if got := mgr.resolveOAuthModelAliasWithResult(auth, "GPT-REAL-UPSTREAM").UpstreamModel; got != "GPT-REAL-UPSTREAM" {
		t.Fatalf("upstream-direct (case) upstream = %q, want GPT-REAL-UPSTREAM", got)
	}
	// A thinking suffix on a direct upstream model must be preserved.
	if got := mgr.resolveOAuthModelAliasWithResult(auth, "gpt-real-upstream(high)").UpstreamModel; got != "gpt-real-upstream(high)" {
		t.Fatalf("upstream-direct with suffix upstream = %q, want gpt-real-upstream(high)", got)
	}
	// The configured alias must still resolve to the upstream model.
	if got := mgr.resolveOAuthModelAliasWithResult(auth, "pretty-alias").UpstreamModel; got != "gpt-real-upstream" {
		t.Fatalf("alias upstream = %q, want gpt-real-upstream", got)
	}
}

// TestOAuthAliasCache_SnapshotReplaceInvalidates verifies that replacing the alias
// table via SetOAuthModelAlias publishes a fresh snapshot whose memo does not
// serve stale results from the previous snapshot.
func TestOAuthAliasCache_SnapshotReplaceInvalidates(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	auth := &Auth{ID: "alias-cache-replace", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}

	mgr.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "old-upstream", Alias: "the-alias"}},
	})
	if got := mgr.resolveOAuthModelAliasWithResult(auth, "the-alias").UpstreamModel; got != "old-upstream" {
		t.Fatalf("before replace upstream = %q, want old-upstream", got)
	}
	oldTable := loadOAuthAliasTableForTest(t, mgr)
	if got := oldTable.tableOnlyComputeCount(); got != 1 {
		t.Fatalf("old snapshot compute count = %d, want 1", got)
	}

	// Replace the snapshot with a new mapping for the same alias name.
	mgr.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "new-upstream", Alias: "the-alias"}},
	})
	got := mgr.resolveOAuthModelAliasWithResult(auth, "the-alias").UpstreamModel
	if got != "new-upstream" {
		t.Fatalf("after snapshot replace upstream = %q, want new-upstream (stale memo)", got)
	}
	newTable := loadOAuthAliasTableForTest(t, mgr)
	if newTable == oldTable {
		t.Fatal("SetOAuthModelAlias did not publish a new table snapshot")
	}
	// Fresh snapshot must have computed once for this key, not inherited the old count.
	if got := newTable.tableOnlyComputeCount(); got != 1 {
		t.Fatalf("new snapshot compute count = %d, want 1", got)
	}
}

// TestOAuthAliasCache_NoCrossChannelContamination verifies that cached resolutions
// are keyed at least by (channel, requestedModel) so two channels sharing an alias
// name but mapping to different upstreams never contaminate each other.
func TestOAuthAliasCache_NoCrossChannelContamination(t *testing.T) {
	t.Parallel()

	mgr := newAliasTestManager(t, map[string][]internalconfig.OAuthModelAlias{
		"codex":  {{Name: "codex-upstream", Alias: "shared-alias"}},
		"claude": {{Name: "claude-upstream", Alias: "shared-alias"}},
	})
	codexAuth := &Auth{ID: "alias-cache-codex", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}
	claudeAuth := &Auth{ID: "alias-cache-claude", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}

	codexRes := mgr.resolveOAuthModelAliasWithResult(codexAuth, "shared-alias").UpstreamModel
	claudeRes := mgr.resolveOAuthModelAliasWithResult(claudeAuth, "shared-alias").UpstreamModel
	if codexRes != "codex-upstream" {
		t.Fatalf("codex channel upstream = %q, want codex-upstream", codexRes)
	}
	if claudeRes != "claude-upstream" {
		t.Fatalf("claude channel upstream = %q, want claude-upstream", claudeRes)
	}

	// Repeated lookups must keep channel-scoped results.
	codexAgain := mgr.resolveOAuthModelAliasWithResult(codexAuth, "shared-alias").UpstreamModel
	claudeAgain := mgr.resolveOAuthModelAliasWithResult(claudeAuth, "shared-alias").UpstreamModel
	if codexAgain != "codex-upstream" || claudeAgain != "claude-upstream" {
		t.Fatalf("cross-channel contamination: codex=%q claude=%q", codexAgain, claudeAgain)
	}

	// Two distinct channels => two distinct computations.
	table := loadOAuthAliasTableForTest(t, mgr)
	if got := table.tableOnlyComputeCount(); got != 2 {
		t.Fatalf("compute count = %d, want 2 (one per channel)", got)
	}
}

// TestOAuthAliasCache_PerAuthAttributePathIsolated verifies that the per-auth
// attribute alias path (which depends on auth identity) is handled outside the
// table-keyed cache and never lets one auth's attribute aliases contaminate
// another auth's resolution.
func TestOAuthAliasCache_PerAuthAttributePathIsolated(t *testing.T) {
	t.Parallel()

	mgr := newAliasTestManager(t, map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "table-upstream", Alias: "shared-alias"}},
	})

	// Auth A carries a per-auth model_aliases attribute that overrides the table.
	authA := &Auth{
		ID:       "alias-cache-attr-a",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind":     "oauth",
			"model_aliases": `[{"name":"attr-upstream-a","alias":"shared-alias"}]`,
		},
	}
	// Auth B has no per-auth aliases and must resolve via the table.
	authB := &Auth{
		ID:       "alias-cache-attr-b",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	if got := mgr.resolveOAuthModelAliasWithResult(authA, "shared-alias").UpstreamModel; got != "attr-upstream-a" {
		t.Fatalf("authA upstream = %q, want attr-upstream-a", got)
	}
	if got := mgr.resolveOAuthModelAliasWithResult(authB, "shared-alias").UpstreamModel; got != "table-upstream" {
		t.Fatalf("authB upstream = %q, want table-upstream", got)
	}

	// Repeat in reverse order: the per-auth path must keep winning for authA and
	// the table memo must keep serving authB without cross-auth contamination.
	if got := mgr.resolveOAuthModelAliasWithResult(authB, "shared-alias").UpstreamModel; got != "table-upstream" {
		t.Fatalf("authB repeat upstream = %q, want table-upstream", got)
	}
	if got := mgr.resolveOAuthModelAliasWithResult(authA, "shared-alias").UpstreamModel; got != "attr-upstream-a" {
		t.Fatalf("authA repeat upstream = %q, want attr-upstream-a", got)
	}
}

// TestOAuthAliasCache_ConcurrentLookupIsSafe exercises the snapshot-local memo
// under concurrency to guard against data races during double-fill.
func TestOAuthAliasCache_ConcurrentLookupIsSafe(t *testing.T) {
	t.Parallel()

	mgr := newAliasTestManager(t, map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5-real", Alias: "gpt-5-alias"}},
	})
	auth := &Auth{ID: "alias-cache-concurrent", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				got := mgr.resolveOAuthModelAliasWithResult(auth, "gpt-5-alias").UpstreamModel
				if got != "gpt-5-real" {
					t.Errorf("concurrent lookup upstream = %q, want gpt-5-real", got)
					return
				}
			}
		}()
	}
	wg.Wait()

	table := loadOAuthAliasTableForTest(t, mgr)
	// Regardless of benign double-fill races, at least one computation happened.
	if got := table.tableOnlyComputeCount(); got < 1 {
		t.Fatalf("compute count = %d, want >= 1", got)
	}
}

// TestOAuthAliasCache_BoundsClientControlledNegativeEntries verifies the memo is
// bounded so client-controlled requested model names cannot grow it without limit.
func TestOAuthAliasCache_BoundsClientControlledNegativeEntries(t *testing.T) {
	t.Parallel()

	mgr := newAliasTestManager(t, map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5-real", Alias: "gpt-5-alias"}},
	})
	auth := &Auth{ID: "alias-cache-bounded", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}

	for i := 0; i < oauthAliasCacheMaxEntries+1; i++ {
		requested := fmt.Sprintf("unknown-model-%d", i)
		if got := mgr.resolveOAuthModelAliasWithResult(auth, requested).UpstreamModel; got != "" {
			t.Fatalf("negative lookup %q upstream = %q, want empty", requested, got)
		}
	}

	table := loadOAuthAliasTableForTest(t, mgr)
	table.cacheMu.RLock()
	cacheLen := len(table.cache)
	table.cacheMu.RUnlock()
	if cacheLen > oauthAliasCacheMaxEntries {
		t.Fatalf("cache len = %d, want <= %d", cacheLen, oauthAliasCacheMaxEntries)
	}
}

// TestOAuthAliasLog_NoSecretLeak_UsesSafeIdentity verifies that the debug logging
// path in oauth_model_alias.go never emits auth attributes/metadata/api-key/
// header/token material, and that it surfaces only the safe identifiers
// (provider/channel/auth id).
func TestOAuthAliasLog_NoSecretLeak_UsesSafeIdentity(t *testing.T) {
	// Non-parallel: temporarily redirects the shared logrus standard logger.
	var buf bytes.Buffer
	prevOut := log.StandardLogger().Out
	prevLevel := log.GetLevel()
	log.SetOutput(&buf)
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetLevel(prevLevel)
	})

	const apiKeySecret = "sk-live-oauth-secret-token-9f3a"
	const headerSecret = "Bearer super-sensitive-bearer"
	const metadataTokenSecret = "metadata-refresh-token-zzz"

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5-real", Alias: "gpt-5-alias"}},
	})
	auth := &Auth{
		ID:       "alias-log-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind":     "oauth",
			"api_key":       apiKeySecret,
			"Authorization": headerSecret,
		},
		Metadata: map[string]any{
			"access_token":  metadataTokenSecret,
			"refresh_token": metadataTokenSecret,
		},
	}

	// Exercise both the debug-logging entry point and the table resolution path.
	_ = mgr.applyOAuthModelAlias(auth, "gpt-5-alias")
	_ = mgr.resolveOAuthModelAliasWithResult(auth, "unknown-model")

	out := buf.String()
	for _, secret := range []string{apiKeySecret, headerSecret, metadataTokenSecret} {
		if strings.Contains(out, secret) {
			t.Fatalf("debug log leaked credential %q:\n%s", secret, out)
		}
	}
	// Attribute keys that carry credentials must not be rendered either.
	for _, attrKey := range []string{"api_key", "Authorization", "access_token", "refresh_token"} {
		if strings.Contains(out, attrKey+"=") || strings.Contains(out, attrKey+":") {
			t.Fatalf("debug log leaked credential attribute key %q:\n%s", attrKey, out)
		}
	}
	// Requirement: the log uses only the safe identifiers provider/channel/auth id.
	if !strings.Contains(out, auth.ID) {
		t.Fatalf("expected safe auth id %q in debug log, got:\n%s", auth.ID, out)
	}
	if !strings.Contains(out, auth.Provider) {
		t.Fatalf("expected provider %q in debug log, got:\n%s", auth.Provider, out)
	}
}

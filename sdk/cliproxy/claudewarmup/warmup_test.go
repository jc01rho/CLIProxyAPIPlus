package claudewarmup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

var fixedNow = time.Date(2026, 10, 29, 7, 50, 0, 0, time.UTC)

func claudeOAuth(id, baseURL string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:         id,
		Provider:   "claude",
		Label:      id + "@example.com",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": baseURL},
		// skip_account_profile keeps request-auth preparation offline (no profile fetch).
		Metadata: map[string]any{"access_token": "sk-ant-oat01-" + id, "email": id + "@example.com", "type": "claude", "skip_account_profile": true},
	}
}

func TestEligibleFiltersClaudeOAuthAccounts(t *testing.T) {
	now := fixedNow
	ok := claudeOAuth("a-ok", "")
	disabled := claudeOAuth("b-disabled", "")
	disabled.Disabled = true
	statusDisabled := claudeOAuth("c-status-disabled", "")
	statusDisabled.Status = coreauth.StatusDisabled
	apiKey := &coreauth.Auth{ID: "d-apikey", Provider: "claude", Attributes: map[string]string{"api_key": "sk-ant-api-x"}}
	codex := claudeOAuth("e-codex", "")
	codex.Provider = "codex"
	weeklySignal := claudeOAuth("f-weekly-signal", "")
	weeklySignal.Quota.Signals = map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Status": "rejected",
		"Anthropic-Ratelimit-Unified-7d-Reset":  strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10),
	}
	weeklyExpired := claudeOAuth("g-weekly-expired", "")
	weeklyExpired.Quota.Signals = map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Status": "rejected",
		"Anthropic-Ratelimit-Unified-7d-Reset":  strconv.FormatInt(now.Add(-time.Hour).Unix(), 10),
	}
	weeklyCooldown := claudeOAuth("h-weekly-cooldown", "")
	weeklyCooldown.Quota = coreauth.QuotaState{Exceeded: true, NextRecoverAt: now.Add(3 * 24 * time.Hour)}
	fiveHourCooldown := claudeOAuth("i-5h-cooldown", "")
	fiveHourCooldown.Quota = coreauth.QuotaState{Exceeded: true, NextRecoverAt: now.Add(2 * time.Hour)}

	got := Eligible([]*coreauth.Auth{fiveHourCooldown, weeklyCooldown, weeklyExpired, weeklySignal, codex, apiKey, statusDisabled, disabled, ok, nil}, now)
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	want := []string{"a-ok", "g-weekly-expired", "i-5h-cooldown"}
	if len(ids) != len(want) {
		t.Fatalf("eligible = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("eligible = %v, want %v", ids, want)
		}
	}
}

type upstreamRecorder struct {
	mu       sync.Mutex
	bodies   map[string][]string // bearer token -> request bodies
	agents   []string
	statuses map[string][]int // bearer token -> scripted statuses
}

func (u *upstreamRecorder) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		token := r.Header.Get("Authorization")
		u.mu.Lock()
		u.bodies[token] = append(u.bodies[token], string(body))
		u.agents = append(u.agents, r.Header.Get("User-Agent"))
		status := http.StatusOK
		if script := u.statuses[token]; len(script) > 0 {
			status = script[0]
			u.statuses[token] = script[1:]
		}
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(time.Now().Add(5*time.Hour).Unix(), 10))
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"scripted failure"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	}
}

func newWarmupManager(t *testing.T, auths ...*coreauth.Auth) *coreauth.Manager {
	t.Helper()
	mgr := coreauth.NewManager(nil, nil, nil)
	mgr.RegisterExecutor(executor.NewClaudeExecutor(&config.Config{}))
	for _, a := range auths {
		if _, err := mgr.Register(coreauth.WithSkipPersist(context.Background()), a); err != nil {
			t.Fatalf("Register(%s): %v", a.ID, err)
		}
	}
	return mgr
}

func noSleep(context.Context, time.Duration) error { return nil }

func TestRunInvokesExecutorOncePerEligibleAccount(t *testing.T) {
	rec := &upstreamRecorder{bodies: map[string][]string{}, statuses: map[string][]int{}}
	server := httptest.NewServer(rec.handler(t))
	defer server.Close()

	disabled := claudeOAuth("acct-disabled", server.URL)
	disabled.Disabled = true
	apiKey := &coreauth.Auth{ID: "acct-apikey", Provider: "claude", Attributes: map[string]string{"api_key": "sk-ant-api-key", "base_url": server.URL}}
	mgr := newWarmupManager(t, claudeOAuth("acct-1", server.URL), claudeOAuth("acct-2", server.URL), disabled, apiKey)

	var sleeps []time.Duration
	results, err := Run(context.Background(), mgr, config.ClaudeWarmupConfig{}, Options{
		Now:   func() time.Time { return fixedNow },
		Sleep: func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil },
		RandN: func(n int64) int64 { return n / 2 },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 2 || !results[0].Success || !results[1].Success {
		t.Fatalf("results = %+v", results)
	}
	if len(sleeps) != 1 || sleeps[0] != 30*time.Second {
		t.Fatalf("jitter sleeps = %v, want [30s]", sleeps)
	}
	if len(rec.bodies) != 2 {
		t.Fatalf("upstream saw %d credentials, want 2: %v", len(rec.bodies), rec.bodies)
	}
	for _, id := range []string{"acct-1", "acct-2"} {
		bodies := rec.bodies["Bearer sk-ant-oat01-"+id]
		if len(bodies) != 1 {
			t.Fatalf("%s: %d requests, want 1", id, len(bodies))
		}
		if got := gjson.Get(bodies[0], "max_tokens").Int(); got != 25 {
			t.Fatalf("%s: max_tokens = %d, want 25; body=%s", id, got, bodies[0])
		}
		if got := gjson.Get(bodies[0], "model").String(); got != config.DefaultClaudeWarmupModel {
			t.Fatalf("%s: model = %q", id, got)
		}
	}
	// The OAuth path presents itself as the Claude Code CLI.
	for _, agent := range rec.agents {
		if !strings.HasPrefix(agent, "claude-cli/") {
			t.Fatalf("User-Agent = %q, want Claude Code CLI", agent)
		}
	}
	// MarkResult recorded the response rate-limit headers on the auth.
	updated, ok := mgr.GetByID("acct-1")
	if !ok || updated.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"] != "allowed" {
		t.Fatalf("quota signals not observed: %+v", updated.Quota)
	}
}

func TestRunRetriesFailureOnceButNotRateLimit(t *testing.T) {
	rec := &upstreamRecorder{bodies: map[string][]string{}, statuses: map[string][]int{
		"Bearer sk-ant-oat01-flaky":   {http.StatusInternalServerError},
		"Bearer sk-ant-oat01-limited": {http.StatusTooManyRequests},
		"Bearer sk-ant-oat01-broken":  {http.StatusInternalServerError, http.StatusInternalServerError},
	}}
	server := httptest.NewServer(rec.handler(t))
	defer server.Close()
	mgr := newWarmupManager(t, claudeOAuth("flaky", server.URL), claudeOAuth("limited", server.URL), claudeOAuth("broken", server.URL))

	results, err := Run(context.Background(), mgr, config.ClaudeWarmupConfig{}, Options{NoJitter: true, Now: func() time.Time { return fixedNow }, Sleep: noSleep})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	byID := map[string]AccountResult{}
	for _, r := range results {
		byID[r.AuthID] = r
	}
	if r := byID["flaky"]; !r.Success || r.Attempts != 2 {
		t.Fatalf("flaky = %+v, want success after 2 attempts", r)
	}
	if r := byID["limited"]; r.Success || r.Attempts != 1 || r.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("limited = %+v, want one 429 attempt", r)
	}
	if r := byID["broken"]; r.Success || r.Attempts != 2 || r.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("broken = %+v, want two 500 attempts", r)
	}
	for token, want := range map[string]int{"Bearer sk-ant-oat01-flaky": 2, "Bearer sk-ant-oat01-limited": 1, "Bearer sk-ant-oat01-broken": 2} {
		if got := len(rec.bodies[token]); got != want {
			t.Fatalf("%s: %d upstream calls, want %d", token, got, want)
		}
	}
}

func TestRunRejectsConcurrentRun(t *testing.T) {
	runGuard.Lock()
	defer runGuard.Unlock()
	if _, err := Run(context.Background(), newWarmupManager(t), config.ClaudeWarmupConfig{}, Options{}); err != ErrAlreadyRunning {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestDefaultModelIsRegisteredClaudeHaiku(t *testing.T) {
	for _, m := range registry.GetClaudeModels() {
		if m != nil && m.ID == config.DefaultClaudeWarmupModel {
			return
		}
	}
	t.Fatalf("default warmup model %q is not in the Claude registry", config.DefaultClaudeWarmupModel)
}

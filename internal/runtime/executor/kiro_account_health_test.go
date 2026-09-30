package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestKiroAccountHealthQuotaCircuitAndLastResort(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := newKiroAccountHealthRegistry(func() time.Time { return now }, func() float64 { return 0.5 })
	h.observeQuota("a", 0, now.Add(time.Hour), time.Minute)
	if err := h.admit("a"); err != nil {
		t.Fatalf("single depleted account must be last-resort eligible: %v", err)
	}
	h.observeQuota("b", 0, now.Add(time.Hour), time.Minute)
	if err := h.admit("a"); err != nil {
		t.Fatalf("all depleted must be last-resort eligible: %v", err)
	}
	h.observeQuota("b", 1, now.Add(time.Hour), time.Minute)
	if err := h.admit("a"); err == nil {
		t.Fatal("healthy sibling must re-exclude depleted account")
	}
	h.failure("b", http.StatusTooManyRequests, []byte(`{"reason":"MONTHLY_REQUEST_COUNT"}`))
	if h.byID["b"].consecutiveFailures != 0 {
		t.Fatal("monthly quota must not count as circuit failure")
	}
	h.failure("b", http.StatusBadGateway, nil)
	if got := h.byID["b"].circuitUntil.Sub(now); got != time.Minute {
		t.Fatalf("first circuit delay = %v", got)
	}
	h.failure("b", http.StatusBadGateway, nil)
	if got := h.byID["b"].circuitUntil.Sub(now); got != 2*time.Minute {
		t.Fatalf("second circuit delay = %v", got)
	}
	if err := h.admit("b"); err == nil {
		t.Fatal("rng above probe threshold should reject open circuit")
	}
	h.rng = func() float64 { return 0.05 }
	if err := h.admit("b"); err != nil {
		t.Fatalf("injected 10%% probe should pass: %v", err)
	}
	now = now.Add(2*time.Minute + time.Second)
	if err := h.admit("b"); err != nil {
		t.Fatalf("expired circuit should pass: %v", err)
	}
}

func TestKiroAccountHealthSaturationReleasesOnStreamTerminalAndCancel(t *testing.T) {
	h := newKiroAccountHealthRegistry(time.Now, func() float64 { return 0 })
	release, err := h.acquire("a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.acquire("a", 1); err == nil {
		t.Fatal("saturated account accepted another request")
	}
	ctx, cancel := context.WithCancel(kiroHealthTestContext(t))
	defer cancel()
	in := make(chan cliproxyexecutor.StreamChunk, 1)
	out := wrapKiroHealthStream(ctx, in, release, func(err error) error { return err }, cancel)
	in <- cliproxyexecutor.StreamChunk{Err: errors.New("upstream gone")}
	select {
	case chunk := <-out:
		if chunk.Err == nil {
			t.Fatal("terminal stream error was lost")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	reacquired, err := h.acquire("a", 1)
	if err != nil {
		t.Fatalf("terminal stream did not release capacity: %v", err)
	}
	reacquired()
	for range out {
	}
	ctx, cancel = context.WithCancel(kiroHealthTestContext(t))
	defer cancel()
	release, err = h.acquire("b", 1)
	if err != nil {
		t.Fatal(err)
	}
	in = make(chan cliproxyexecutor.StreamChunk)
	out = wrapKiroHealthStream(ctx, in, release, func(err error) error { return err }, cancel)
	cancel()
	for range out {
	}
	reacquired, err = h.acquire("b", 1)
	if err != nil {
		t.Fatalf("cancelled stream did not release capacity: %v", err)
	}
	reacquired()
}

func TestKiroRefreshLeaseRejectsConcurrentProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kiro.json")
	cmd := exec.CommandContext(kiroHealthTestContext(t), os.Args[0], "-test.run=^TestKiroRefreshLeaseHelper$", "--")
	cmd.Env = append(os.Environ(), "KIRO_LEASE_HELPER="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := stdin.Write([]byte("stop\n")); err != nil {
			t.Error(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("lease helper = %q, %v", line, err)
	}
	lease, err := acquireKiroRefreshLease(path)
	if err == nil {
		_ = lease.Close()
		t.Fatal("second process acquired refresh lease")
	}
	var scoped interface{ IsCredentialScoped() bool }
	if !errors.As(err, &scoped) || !scoped.IsCredentialScoped() {
		t.Fatalf("lease contention error = %v", err)
	}
}

func TestKiroRefreshLeaseHelper(t *testing.T) {
	path := os.Getenv("KIRO_LEASE_HELPER")
	if path == "" {
		return
	}
	lease, err := acquireKiroRefreshLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lease.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := os.Stdout.WriteString("ready\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
}

func TestKiroRefreshLeaseRejectsStaleLineageAndPersistsResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kiro.json")
	if err := os.WriteFile(path, []byte(`{"refresh_token":"refresh","access_token":"old","client_id":"client","client_secret":"secret","auth_method":"builder-id"}`), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "new", "refreshToken": "rotated", "expiresIn": 3600})
	}))
	defer server.Close()
	e := NewKiroExecutor(&config.Config{OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{"kiro": {ApiBaseURL: server.URL, TokenURL: server.URL + "/token"}}})
	auth := &cliproxyauth.Auth{ID: "a", Attributes: map[string]string{"path": path}, Metadata: map[string]any{"refresh_token": "refresh", "access_token": "old"}}
	updated, err := e.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := readKiroMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored["access_token"] != "new" || stored[kiroLineageKey] == "" {
		t.Fatalf("persisted refresh = %#v", stored)
	}
	auth.Metadata[kiroLineageKey] = "replaced"
	if _, err := e.Refresh(context.Background(), auth); !errors.Is(err, errKiroCredentialReplaced) {
		t.Fatalf("stale lineage error = %v", err)
	}
	if updated.Metadata["refresh_token"] != "rotated" {
		t.Fatalf("updated token = %#v", updated.Metadata)
	}
}

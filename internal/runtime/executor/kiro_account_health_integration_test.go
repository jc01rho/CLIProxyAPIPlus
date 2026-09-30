package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type kiroHealthTransport func(*http.Request) (*http.Response, error)

func (f kiroHealthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func kiroHealthTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func kiroHealthAssertRetry(t *testing.T, err error, delay time.Duration) {
	t.Helper()
	var scoped interface {
		IsCredentialScoped() bool
		RetryAfter() *time.Duration
	}
	if !errors.As(err, &scoped) || !scoped.IsCredentialScoped() || scoped.RetryAfter() == nil || *scoped.RetryAfter() != delay {
		t.Fatalf("error = %v, want credential scope and RetryAfter %v", err, delay)
	}
}

func TestKiroHealthQuotaExpiryMeteringAndCircuitCap(t *testing.T) {
	now := time.Unix(1700000000, 0)
	h := newKiroAccountHealthRegistry(func() time.Time { return now }, func() float64 { return 0.1 })
	h.observeQuota("healthy", 50, time.Time{}, time.Hour)
	h.observeQuota("depleted", 0, time.Time{}, time.Minute)
	kiroHealthAssertRetry(t, h.admit("depleted"), 2*time.Minute)
	now = now.Add(2 * time.Minute)
	if err := h.admit("depleted"); err != nil {
		t.Fatal(err)
	}
	h.observeQuota("depleted", 0, now.Add(time.Second), time.Hour)
	kiroHealthAssertRetry(t, h.admit("depleted"), time.Second)
	now = now.Add(time.Second)
	if err := h.admit("depleted"); err != nil {
		t.Fatal(err)
	}
	h.observeQuota("metered", 1.5, time.Time{}, time.Minute)
	ctx := context.WithValue(context.Background(), kiroQuotaContextKey{}, kiroQuotaRecorder{h, "metered"})
	consumeKiroQuota(ctx, 1)
	if err := h.admit("metered"); err != nil {
		t.Fatal(err)
	}
	consumeKiroQuota(ctx, 0.5)
	kiroHealthAssertRetry(t, h.admit("metered"), 2*time.Minute)
	for n := 1; n <= 14; n++ {
		err := h.result("breaker", context.Background(), statusErr{code: 502})
		kiroHealthAssertRetry(t, err, time.Minute*time.Duration(min(1<<(n-1), 1440)))
	}
	before := h.byID["breaker"].consecutiveFailures
	for _, err := range []error{statusErr{code: 400}, statusErr{code: 422}, statusErr{code: 429, msg: `{"reason":"USER_REQUEST_RATE_EXCEEDED"}`}, context.Canceled} {
		h.result("breaker", context.Background(), err)
	}
	if h.byID["breaker"].consecutiveFailures != before {
		t.Fatal("nonrecoverable/rate errors incremented circuit")
	}
	h.result("breaker", context.Background(), nil)
	if h.byID["breaker"].consecutiveFailures != 0 {
		t.Fatal("success did not reset circuit")
	}
}

func TestKiroHealthUsageCheckFeedsRegistry(t *testing.T) {
	now := time.Unix(1700000000, 0)
	h := newKiroAccountHealthRegistry(func() time.Time { return now }, func() float64 { return 1 })
	e := NewKiroExecutor(&config.Config{})
	e.health = h
	var calls atomic.Int32
	ctx := context.WithValue(kiroHealthTestContext(t), "cliproxy.roundtripper", http.RoundTripper(kiroHealthTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("X-Amz-Target") != "AmazonCodeWhispererService.GetUsageLimits" {
			t.Errorf("unexpected usage target %q", r.Header.Get("X-Amz-Target"))
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"nextDateReset":1700000060,"usageBreakdownList":[{"usageLimitWithPrecision":10,"currentUsageWithPrecision":10}]}`))}, nil
	})))
	auth := &cliproxyauth.Auth{ID: t.Name(), Metadata: map[string]any{"access_token": "token", "auth_method": "builder-id"}}
	e.refreshKiroQuota(ctx, auth)
	e.refreshKiroQuota(ctx, auth)
	h.observeQuota("healthy", 1, time.Time{}, time.Minute)
	kiroHealthAssertRetry(t, h.admit(auth.ID), time.Minute)
	if calls.Load() != 1 {
		t.Fatalf("usage checks = %d", calls.Load())
	}
}

func TestKiroHealthExecuteLifecycle(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, sdktranslator.FromString("openai-response")} {
		for _, mode := range []string{"nonstream", "end", "error", "cancel"} {
			t.Run(format.String()+"/"+mode, func(t *testing.T) {
				ctx := kiroHealthTestContext(t)
				h := newKiroAccountHealthRegistry(time.Now, func() float64 { return 1 })
				e := NewKiroExecutor(&config.Config{KiroMaxInflight: 1})
				e.health = h
				e.retryWait = func(context.Context, time.Duration) error { return nil }
				auth := &cliproxyauth.Auth{ID: t.Name(), Metadata: map[string]any{"access_token": t.Name(), "auth_method": "builder-id"}}
				// Suppress unrelated maintenance in this lifecycle test. A separate test
				// above exercises the real usage checker and its admission observations.
				h.stateLocked(auth.ID).nextUsageCheck = time.Now().Add(time.Hour)
				reader, writer := io.Pipe()
				t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
				entered := make(chan struct{})
				closed := make(chan struct{})
				body := &kiroHealthCloseSignal{ReadCloser: reader, closed: closed}
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(kiroHealthTransport(func(r *http.Request) (*http.Response, error) {
					close(entered)
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
				})))
				payload := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"Hi"}]}`)
				if format.String() == "openai-response" {
					payload = []byte(`{"model":"claude-sonnet-4.5","input":"Hi"}`)
				}
				req := cliproxyexecutor.Request{Model: "claude-sonnet-4.5", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: payload}
				successfulStream := `{"assistantResponseEvent":{"content":"answer"}}{"messageStopEvent":{"stopReason":"END_TURN"}}`
				if mode == "nonstream" {
					done := make(chan error, 1)
					go func() {
						resp, err := e.Execute(ctx, auth, req, opts)
						if err == nil && len(resp.Payload) == 0 {
							err = errors.New("empty response")
						}
						done <- err
					}()
					select {
					case <-entered:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					_, err := e.Execute(ctx, auth, req, opts)
					kiroHealthAssertRetry(t, err, time.Second)
					if _, err = io.WriteString(writer, successfulStream); err != nil {
						t.Fatal(err)
					}
					if err = writer.Close(); err != nil {
						t.Fatal(err)
					}
					select {
					case err = <-done:
						if err != nil {
							t.Fatal(err)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				} else {
					streamCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					stream, err := e.ExecuteStream(streamCtx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					select {
					case <-entered:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					_, err = e.ExecuteStream(ctx, auth, req, opts)
					kiroHealthAssertRetry(t, err, time.Second)
					drained := make(chan []cliproxyexecutor.StreamChunk, 1)
					go func() {
						var chunks []cliproxyexecutor.StreamChunk
						for chunk := range stream.Chunks {
							chunks = append(chunks, chunk)
						}
						drained <- chunks
					}()
					switch mode {
					case "cancel":
						cancel()
					case "error":
						if err := writer.CloseWithError(io.ErrUnexpectedEOF); err != nil {
							t.Fatal(err)
						}
					case "end":
						if _, err := io.WriteString(writer, successfulStream); err != nil {
							t.Fatal(err)
						}
						if err := writer.Close(); err != nil {
							t.Fatal(err)
						}
					}
					select {
					case chunks := <-drained:
						hasError := false
						for _, chunk := range chunks {
							if chunk.Err != nil {
								hasError = true
							}
						}
						if mode == "error" && !hasError {
							t.Fatal("stream lost upstream error")
						}
						if mode == "end" && (hasError || len(chunks) == 0) {
							t.Fatalf("bad successful stream: %v", chunks)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				select {
				case <-closed:
				case <-ctx.Done():
					t.Fatal("upstream body leaked")
				}
				h.mu.Lock()
				inflight := h.byID[auth.ID].inflight
				h.mu.Unlock()
				if inflight != 0 {
					t.Fatalf("inflight leaked: %d", inflight)
				}
				kiroauth.GetGlobalRateLimiter().ClearTokenState(getAccountKey(auth))
			})
		}
	}
}

type kiroHealthCloseSignal struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

func (b *kiroHealthCloseSignal) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { close(b.closed) })
	return err
}

func TestKiroRefreshReplacementDuringRequestAndLaterSave(t *testing.T) {
	for _, replacement := range []string{"lineage", "token", "removed", "later-save"} {
		t.Run(replacement, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kiro.json")
			metadata := map[string]any{"access_token": "old", "refresh_token": "old-refresh", "client_id": "client", "client_secret": "secret", "auth_method": "builder-id", kiroLineageKey: "lineage:original", "profile_arn": kiroBuilderIDProfileARN}
			if err := writeKiroMetadata(path, metadata); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch replacement {
				case "lineage":
					metadata[kiroLineageKey] = "lineage:replacement"
					metadata["refresh_token"] = "replacement"
				case "token":
					metadata["refresh_token"] = "replacement"
				case "removed":
					if err := os.Remove(path); err != nil {
						t.Error(err)
					}
				}
				if replacement != "removed" && replacement != "later-save" {
					if err := writeKiroMetadata(path, metadata); err != nil {
						t.Error(err)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "stale-result", "refreshToken": "rotated", "expiresIn": 3600})
			}))
			defer server.Close()
			e := NewKiroExecutor(&config.Config{OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{"kiro": {ApiBaseURL: server.URL}}})
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"path": path}, Metadata: map[string]any{kiroLineageKey: "lineage:original"}}
			updated, err := e.Refresh(kiroHealthTestContext(t), auth)
			if replacement == "later-save" {
				if err != nil {
					t.Fatal(err)
				}
				metadata[kiroLineageKey] = "lineage:replacement"
				if err := writeKiroMetadata(path, metadata); err != nil {
					t.Fatal(err)
				}
				err = updated.Storage.SaveTokenToFile(path)
			}
			if replacement == "removed" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("disk failure not surfaced: %v", err)
				}
			} else {
				if !errors.Is(err, errKiroCredentialReplaced) {
					t.Fatalf("replacement error = %v", err)
				}
				stored, readErr := readKiroMetadata(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if stored["access_token"] != "old" {
					t.Fatal("stale result overwrote replacement")
				}
			}
		})
	}
}

func TestKiroMaxInflightConfig(t *testing.T) {
	for _, raw := range []string{"kiro-max-inflight: 2", "oauth:\n  providers:\n    kiro:\n      max-inflight: 2"} {
		cfg, err := config.ParseConfigBytes([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := NewKiroExecutor(cfg).kiroInflightLimit(); got != 2 {
			t.Fatalf("limit = %d", got)
		}
	}
	if got := NewKiroExecutor(&config.Config{}).kiroInflightLimit(); got != 4 {
		t.Fatalf("default limit = %d", got)
	}
}

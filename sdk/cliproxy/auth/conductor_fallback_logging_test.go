package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type fallbackLogOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *fallbackLogOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *fallbackLogOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func captureFallbackLogOutput(t *testing.T, level log.Level) *fallbackLogOutput {
	t.Helper()
	logger := log.StandardLogger()
	out, formatter, previousLevel := logger.Out, logger.Formatter, logger.GetLevel()
	buffer := &fallbackLogOutput{}
	logger.SetOutput(buffer)
	logger.SetFormatter(&logging.LogFormatter{})
	logger.SetLevel(level)
	t.Cleanup(func() {
		logger.SetOutput(out)
		logger.SetFormatter(formatter)
		logger.SetLevel(previousLevel)
	})
	return buffer
}

var fallbackLogFieldPattern = regexp.MustCompile(`([a-z_]+)=("(?:\\.|[^"\\])*"|[0-9]+)`)

func fallbackLogRecords(t *testing.T, output, requestID string) []map[string]string {
	t.Helper()
	var records []map[string]string
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "["+requestID+"]") || !strings.Contains(line, "fallback_source=") {
			continue
		}
		if strings.ContainsAny(line, "\r\t\x1b") {
			t.Fatalf("unescaped control character: %q", line)
		}
		fields := make(map[string]string)
		for _, match := range fallbackLogFieldPattern.FindAllStringSubmatch(line, -1) {
			value := match[2]
			if strings.HasPrefix(value, `"`) {
				var err error
				value, err = strconv.Unquote(value)
				if err != nil {
					t.Fatalf("invalid quoted field %s: %v", match[1], err)
				}
			}
			if _, duplicate := fields[match[1]]; duplicate {
				t.Fatalf("duplicate field %s: %q", match[1], line)
			}
			fields[match[1]] = value
		}
		records = append(records, fields)
	}
	return records
}

func assertFallbackLogFields(t *testing.T, fields, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("%s = %q, want %q; fields=%v", key, fields[key], value, fields)
		}
	}
}

func runFallbackLogRequest(ctx context.Context, manager *Manager, stream bool, original string, attempt func(context.Context, string) error) error {
	req := cliproxyexecutor.Request{Model: original}
	if stream {
		_, err := manager.executeStreamWithRouteFallback(ctx, []string{"log-fixture"}, req, cliproxyexecutor.Options{},
			func(ctx context.Context, _ []string, req cliproxyexecutor.Request, _ cliproxyexecutor.Options, _ int, _ *int, _, _ int) (*cliproxyexecutor.StreamResult, error) {
				if err := attempt(ctx, req.Model); err != nil {
					return nil, err
				}
				chunks := make(chan cliproxyexecutor.StreamChunk)
				close(chunks)
				return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
			})
		return err
	}
	_, err := manager.executeWithRouteFallback(ctx, []string{"log-fixture"}, req, cliproxyexecutor.Options{},
		func(ctx context.Context, _ []string, req cliproxyexecutor.Request, _ cliproxyexecutor.Options, _, _, _ int) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, attempt(ctx, req.Model)
		})
	return err
}

func TestRouteFallbackFormattedLogs(t *testing.T) {
	unavailable := &Error{Code: "auth_unavailable", Message: "no credential"}
	upstream := fmt.Errorf("wrapped: %w", &Error{HTTPStatus: http.StatusServiceUnavailable, Message: `{"error":{"code":"UPSTREAM_503","message":"busy"}}`})
	quota := &Error{HTTPStatus: http.StatusTooManyRequests, Code: "UPSTREAM_429", Message: "quota"}
	for _, stream := range []bool{false, true} {
		for _, level := range []log.Level{log.InfoLevel, log.DebugLevel} {
			for _, tc := range []struct {
				name         string
				errs         []error
				mapping      bool
				causeMarkers []string
			}{
				{name: "statusless", errs: []error{unavailable, nil}, causeMarkers: []string{"auth_unavailable"}},
				{name: "http", errs: []error{upstream, nil}, causeMarkers: []string{"UPSTREAM_503"}},
				{name: "multi-hop", errs: []error{unavailable, quota, nil}, causeMarkers: []string{"auth_unavailable", "UPSTREAM_429"}},
				{name: "mapping-then-chain", mapping: true, errs: []error{upstream, quota, nil}, causeMarkers: []string{"UPSTREAM_503", "UPSTREAM_429"}},
				{name: "nested-unavailable", errs: []error{WithCause(unavailable, upstream), nil}, causeMarkers: []string{"auth_unavailable"}},
				{name: "fallback-canceled", errs: []error{upstream, context.Canceled}, causeMarkers: []string{"UPSTREAM_503"}},
			} {
				t.Run(fmt.Sprintf("stream=%t/%s/%s", stream, level, tc.name), func(t *testing.T) {
					output := captureFallbackLogOutput(t, level)
					const requestID = "fallback-request"
					ctx := logging.WithRequestID(context.Background(), requestID)
					manager := NewManager(nil, nil, nil)
					manager.SetRetryConfig(0, 0, 0)
					models := []string{"original", "middle", "target"}
					manager.SetFallbackChain(models[1:], 3)
					if tc.mapping {
						manager.SetFallbackModels(map[string]string{models[0]: models[1]})
					}
					calls := 0
					wantRecords := 0
					err := runFallbackLogRequest(ctx, manager, stream, models[0], func(ctx context.Context, model string) error {
						i := calls
						calls++
						if i >= len(tc.errs) || model != models[i] {
							t.Fatalf("unexpected attempt %d for %q", i, model)
						}
						if i > 0 {
							wantRecords++
							records := fallbackLogRecords(t, output.String(), requestID)
							if len(records) != wantRecords {
								t.Fatalf("before attempt %s: got %d records, want %d (trigger must precede execution); output=%q", model, len(records), wantRecords, output.String())
							}
							fields := records[len(records)-1]
							source := "fallback-chain"
							if tc.mapping && i == 1 {
								source = "fallback-models"
							}
							assertFallbackLogFields(t, fields, map[string]string{
								"requested_model": models[0], "fallback_trigger_model": models[i-1],
								"selected_fallback_model": model, "fallback_source": source, "outcome": "attempt",
							})
							wantStatus := ""
							if status := statusCodeFromError(tc.errs[i-1]); status > 0 {
								wantStatus = strconv.Itoa(status)
							}
							assertFallbackLogFields(t, fields, map[string]string{"fallback_trigger_status": wantStatus})
							if !strings.Contains(fields["fallback_trigger_error"], tc.causeMarkers[i-1]) {
								t.Errorf("missing cause marker %s: %v", tc.causeMarkers[i-1], fields)
							}
							if tc.name == "nested-unavailable" && !strings.Contains(fields["fallback_trigger_error"], "UPSTREAM_503") {
								t.Errorf("nested cause lost: %v", fields)
							}
							if requested, selected := GetFallbackInfoFromContext(ctx); requested != models[0] || selected != model {
								t.Errorf("fallback context changed: %q -> %q", requested, selected)
							}
							if tc.errs[i] == nil || level == log.DebugLevel {
								wantRecords++
							}
						}
						return tc.errs[i]
					})
					if !errors.Is(err, tc.errs[len(tc.errs)-1]) || calls != len(tc.errs) {
						t.Fatalf("error=%v calls=%d, want error=%v calls=%d", err, calls, tc.errs[len(tc.errs)-1], len(tc.errs))
					}
					records := fallbackLogRecords(t, output.String(), requestID)
					if len(records) != wantRecords {
						t.Fatalf("records=%v, want count %d", records, wantRecords)
					}
					for _, fields := range records {
						if fields["outcome"] == "attempt" {
							continue
						}
						if _, err := strconv.ParseInt(fields["elapsed_ms"], 10, 64); err != nil {
							t.Errorf("elapsed_ms invalid: %v", fields)
						}
						if fields["outcome"] == "success" {
							assertFallbackLogFields(t, fields, map[string]string{"fallback_trigger_model": models[calls-2], "selected_fallback_model": models[calls-1]})
						} else if fields["selected_fallback_model"] == "middle" && len(tc.errs) == 3 {
							assertFallbackLogFields(t, fields, map[string]string{"outcome": "error", "fallback_result_status": "429"})
							if !strings.Contains(fields["fallback_result_error"], "UPSTREAM_429") {
								t.Errorf("missing result cause: %v", fields)
							}
						}
					}
					t.Logf("rendered logs:\n%s", output.String())
				})
			}
		}
	}
}

func TestRouteFallbackNoActivationFormattedLogs(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			err  error
		}{
			{name: "success"}, {name: "canceled", err: context.Canceled}, {name: "deadline", err: context.DeadlineExceeded},
			{name: "no-candidates", err: &Error{Code: "auth_unavailable"}},
			{name: "denied", err: &Error{Code: "auth_unavailable"}},
			{name: "non-model-404", err: &Error{HTTPStatus: 404, Code: "RESOURCE_MISSING"}},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				output := captureFallbackLogOutput(t, log.DebugLevel)
				manager := NewManager(nil, nil, nil)
				manager.SetRetryConfig(0, 0, 0)
				if tc.name != "no-candidates" {
					manager.SetFallbackChain([]string{"target"}, 1)
				}
				if tc.name == "denied" {
					manager.SetFallbackAllowedModels([]string{"different-model"})
				}
				calls := 0
				err := runFallbackLogRequest(logging.WithRequestID(context.Background(), "no-fallback"), manager, stream, "original", func(_ context.Context, model string) error {
					calls++
					if model != "original" {
						t.Fatalf("unexpected fallback to %s", model)
					}
					return tc.err
				})
				if !errors.Is(err, tc.err) || calls != 1 {
					t.Fatalf("error=%v calls=%d", err, calls)
				}
				if output.String() != "" {
					t.Fatalf("unexpected activation logs: %q", output.String())
				}
			})
		}
	}
}

func TestRouteFallbackFormattedLogSafety(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		forbidden     []string
	}{
		{name: "json-envelope", message: `{"error":{"code":"UPSTREAM_FIXTURE","message":"busy"},"request":{"messages":["PAYLOAD_MARKER"]},"api_key":"SECRET_MARKER"}`, forbidden: []string{"PAYLOAD_MARKER", "SECRET_MARKER", `"request"`}},
		{name: "unknown-json", message: `{"request":{"messages":["PAYLOAD_MARKER"]},"api_key":"SECRET_MARKER"}`, forbidden: []string{"PAYLOAD_MARKER", "SECRET_MARKER", `"request"`}},
		{name: "object-message", message: `{"error":{"code":"UPSTREAM_FIXTURE","message":{"input":"PAYLOAD_MARKER"}}}`, forbidden: []string{"PAYLOAD_MARKER"}},
		{name: "prefixed-array", message: `status 503: ["PAYLOAD_MARKER"]`, forbidden: []string{"PAYLOAD_MARKER"}},
		{name: "prefixed-html", message: `status 503: <html>PAYLOAD_MARKER</html>`, forbidden: []string{"PAYLOAD_MARKER"}},
		{name: "malformed-json", message: `status 503: {"input":"PAYLOAD_MARKER"`, forbidden: []string{"PAYLOAD_MARKER"}},
		{name: "bounded-safe-signals", message: `{"detail":"socks authentication failed proxyconnect dial failed connection refused connection reset connection aborted stream reset network is unreachable no such host server misbehaving tls handshake timeout unexpected EOF certificate invalid character cannot unmarshal invalid_grant refresh_token_expired refresh_token_revoked refresh_token_reused PAYLOAD_MARKER"}`, forbidden: []string{"PAYLOAD_MARKER"}},
		{name: "credentials", message: "Authorization: Bearer AUTH_SECRET\nCookie: session=COOKIE_SECRET\napi_key=KEY_SECRET; https://user:PASS_SECRET@example.invalid?token=QUERY_SECRET", forbidden: []string{"AUTH_SECRET", "COOKIE_SECRET", "KEY_SECRET", "PASS_SECRET", "QUERY_SECRET"}},
		{name: "quote-and-bound", message: "UPSTREAM_FIXTURE\r\n\"outcome=success\"\t\x1b[31m " + strings.Repeat("界", 400)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureFallbackLogOutput(t, log.DebugLevel)
			manager := NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.SetFallbackChain([]string{"target\n\"outcome=success\""}, 1)
			fixtureErr := &Error{HTTPStatus: 503, Message: tc.message}
			err := runFallbackLogRequest(logging.WithRequestID(context.Background(), "safe-fallback"), manager, false, "original\r\ninjected", func(context.Context, string) error { return fixtureErr })
			if !errors.Is(err, fixtureErr) {
				t.Fatalf("error changed: %v", err)
			}
			rendered := output.String()
			if strings.Count(rendered, "\n") != 2 {
				t.Errorf("want exactly two lines: %q", rendered)
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(rendered, forbidden) {
					t.Errorf("leaked %q: %q", forbidden, rendered)
				}
			}
			records := fallbackLogRecords(t, rendered, "safe-fallback")
			if len(records) != 2 {
				t.Fatalf("records=%v", records)
			}
			assertFallbackLogFields(t, records[0], map[string]string{"outcome": "attempt", "fallback_trigger_status": "503"})
			assertFallbackLogFields(t, records[1], map[string]string{"outcome": "error", "fallback_result_status": "503"})
			for _, fields := range records {
				for _, key := range []string{"fallback_trigger_error", "fallback_result_error"} {
					if utf8.RuneCountInString(fields[key]) > 256 {
						t.Errorf("unbounded %s: %d runes", key, utf8.RuneCountInString(fields[key]))
					}
				}
			}
		})
	}
}

// This fixture replaces only the provider boundary; selection, retry, fallback,
// stream bootstrap and the application's standard LogFormatter remain real.
type formattedFallbackExecutor struct {
	providerFallbackExecutor
	attempt func(context.Context, string) error
}

func (e *formattedFallbackExecutor) Execute(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.attempt(ctx, req.Model); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	payload, err := json.Marshal(map[string]string{"model": req.Model})
	return cliproxyexecutor.Response{Payload: payload}, err
}

func (e *formattedFallbackExecutor) ExecuteStream(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if err := e.attempt(ctx, req.Model); err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"model":"target"}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestManagerRouteFallbackFormattedInfoEntrypoint(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			output := captureFallbackLogOutput(t, log.InfoLevel)
			const requestID = "entrypoint-fallback"
			ctx, cancel := context.WithTimeout(logging.WithRequestID(context.Background(), requestID), 5*time.Second)
			defer cancel()
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 1)
			manager.SetFallbackChain([]string{"target"}, 1)
			calls := 0
			executor := &formattedFallbackExecutor{providerFallbackExecutor: providerFallbackExecutor{id: "formatted-fixture"}}
			executor.attempt = func(_ context.Context, model string) error {
				calls++
				if model == "original" {
					return &Error{HTTPStatus: 503, Message: `{"error":{"code":"UPSTREAM_FIXTURE","message":"busy"}}`}
				}
				records := fallbackLogRecords(t, output.String(), requestID)
				if len(records) != 1 {
					t.Fatalf("before provider fallback: records=%v output=%q", records, output.String())
				}
				assertFallbackLogFields(t, records[0], map[string]string{"outcome": "attempt", "fallback_trigger_status": "503", "fallback_trigger_model": "original", "selected_fallback_model": "target"})
				return nil
			}
			manager.RegisterExecutor(executor)
			authID := t.Name()
			if _, err := manager.Register(ctx, &Auth{ID: authID, Provider: executor.id, Status: StatusActive}); err != nil {
				t.Fatal(err)
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(authID, executor.id, []*registry.ModelInfo{{ID: "original"}, {ID: "target"}})
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			req := cliproxyexecutor.Request{Model: "original"}
			if stream {
				result, err := manager.ExecuteStream(ctx, []string{executor.id}, req, cliproxyexecutor.Options{})
				if err != nil {
					t.Fatal(err)
				}
				chunks := 0
				for result.Chunks != nil {
					select {
					case chunk, ok := <-result.Chunks:
						if !ok {
							result.Chunks = nil
							break
						}
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						chunks++
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				if chunks != 1 {
					t.Fatalf("chunks=%d", chunks)
				}
			} else {
				response, err := manager.Execute(ctx, []string{executor.id}, req, cliproxyexecutor.Options{})
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]string
				if err := json.Unmarshal(response.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload["model"] != "target" {
					t.Fatalf("payload=%v", payload)
				}
			}
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
			records := fallbackLogRecords(t, output.String(), requestID)
			if len(records) != 2 {
				t.Fatalf("records=%v", records)
			}
			assertFallbackLogFields(t, records[1], map[string]string{"outcome": "success", "fallback_trigger_status": "503", "requested_model": "original", "selected_fallback_model": "target"})
			t.Logf("application Info output:\n%s", output.String())
		})
	}
}

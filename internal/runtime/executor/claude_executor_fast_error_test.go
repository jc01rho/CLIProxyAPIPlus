package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Fast mode is not enabled for proxied requests. A caller asking for it through
// speed:"fast", a body beta or the Anthropic-Beta header is sent upstream as an
// ordinary request, and its errors take the ordinary (non-Fast) error path.
func TestClaudeExecutorFastRequestIsSentAsOrdinaryRequest(t *testing.T) {
	testCases := []struct {
		name     string
		status   int
		stream   bool
		oauth    bool
		betaOnly bool
	}{
		{name: "non-stream OAuth bad request", status: http.StatusBadRequest, oauth: true},
		{name: "stream OAuth unauthorized", status: http.StatusUnauthorized, stream: true, oauth: true},
		{name: "non-stream API key forbidden", status: http.StatusForbidden},
		{name: "stream OAuth beta-only Fast request", status: http.StatusBadRequest, stream: true, oauth: true, betaOnly: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var attempts atomic.Int32
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				attempts.Add(1)
				requestBody, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					t.Fatal(errRead)
				}
				if bytes.Contains(requestBody, []byte(`"speed"`)) {
					t.Fatalf("upstream request unexpectedly carries speed: %s", requestBody)
				}
				for name, values := range req.Header {
					if strings.EqualFold(name, "Anthropic-Beta") && strings.Contains(strings.Join(values, ","), claudeFastModeBeta) {
						t.Fatalf("upstream Anthropic-Beta unexpectedly carries %s: %v", claudeFastModeBeta, values)
					}
				}
				return &http.Response{
					StatusCode: testCase.status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"rejected"}}`)),
					Request:    req,
				}, nil
			})

			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{ID: "fast-ordinary-test", Metadata: claudeOAuthTestMetadata()}
			if testCase.oauth {
				auth.Attributes = map[string]string{"api_key": "sk-ant-oat-fast-ordinary", "cloak_mode": "always"}
			} else {
				auth.Attributes = map[string]string{"api_key": "sk-ant-api03-fast-ordinary"}
				auth.Metadata = nil
			}
			requestPayload := []byte(`{"model":"claude-opus-5","max_tokens":16,"speed":"fast","betas":["fast-mode-2026-02-01"],"messages":[{"role":"user","content":"reply OK"}]}`)
			if testCase.betaOnly {
				requestPayload = []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"reply OK"}]}`)
			}
			options := cliproxyexecutor.Options{
				Stream:         testCase.stream,
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Headers:        http.Header{"Anthropic-Beta": []string{claudeFastModeBeta}},
			}
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: requestPayload}

			executor := NewClaudeExecutor(&config.Config{})
			var errExecute error
			if testCase.stream {
				_, errExecute = executor.ExecuteStream(ctx, auth, request, options)
			} else {
				_, errExecute = executor.Execute(ctx, auth, request, options)
			}
			if errExecute == nil {
				t.Fatal("request error = nil, want the upstream error")
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("upstream attempts = %d, want 1", got)
			}
			var direct *cliproxyexecutor.RequestTerminatedError
			if errors.As(errExecute, &direct) {
				t.Fatalf("error = %T %v, want the ordinary error path instead of a Fast direct response", errExecute, errExecute)
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errExecute, &statusErr) || statusErr.StatusCode() != testCase.status {
				t.Fatalf("error = %v, want status %d", errExecute, testCase.status)
			}
		})
	}
}

func TestClaudeExecutorNonFastErrorKeepsCredentialScopedBehavior(t *testing.T) {
	var attempts atomic.Int32
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "standard-rate-limit",
		Attributes: map[string]string{"api_key": "sk-ant-oat-standard-rate-limit"},
		Metadata:   claudeOAuthTestMetadata(),
	}
	request := cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"reply OK"}]}`),
	}

	_, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, request, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	var statusError interface{ StatusCode() int }
	if !errors.As(errExecute, &statusError) || statusError.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want status 429", errExecute)
	}
	var direct *cliproxyexecutor.RequestTerminatedError
	if errors.As(errExecute, &direct) {
		t.Fatal("non-Fast error unexpectedly became a direct response")
	}
	if requestScoped, ok := errExecute.(cliproxyexecutor.RequestScopedError); ok && requestScoped.IsRequestScoped() {
		t.Fatal("non-Fast rate limit unexpectedly became request-scoped")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("upstream attempts = %d, want 1", got)
	}
}

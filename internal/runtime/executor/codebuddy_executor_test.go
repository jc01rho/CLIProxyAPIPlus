package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type streamCancelRoundTripper func(*http.Request) (*http.Response, error)

func (f streamCancelRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type codeBuddyCloseSignalReadCloser struct {
	reader io.Reader
	closed chan struct{}
}

func newCodeBuddyCloseSignalReadCloser(body string) *codeBuddyCloseSignalReadCloser {
	return &codeBuddyCloseSignalReadCloser{
		reader: strings.NewReader(body),
		closed: make(chan struct{}),
	}
}

func (b *codeBuddyCloseSignalReadCloser) Read(p []byte) (int, error) {
	return b.reader.Read(p)
}

func (b *codeBuddyCloseSignalReadCloser) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func TestCodeBuddyExecutorExecuteStream_closesBody_whenDownstreamCancelsAfterFirstChunk(t *testing.T) {
	// Given
	body := newCodeBuddyCloseSignalReadCloser(strings.Join([]string{
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"first"}}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"second"}}]}`,
		"",
	}, "\n"))
	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", streamCancelRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
			Request:    req,
		}, nil
	}))

	exec := NewCodeBuddyExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "access-token",
			"user_id":      "user-id",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}

	// When
	result, err := exec.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before first chunk")
		}
		if len(chunk.Payload) == 0 {
			t.Fatal("first chunk payload is empty")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first chunk")
	}
	cancel()

	// Then
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("stream body was not closed after downstream cancellation")
	}
	select {
	case _, ok := <-result.Chunks:
		if ok {
			t.Fatal("stream channel remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("stream producer did not exit after downstream cancellation")
	}
}

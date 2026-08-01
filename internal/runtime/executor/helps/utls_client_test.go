package helps

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewUtlsHTTPClientUsesContextRoundTripperForProtectedHost(t *testing.T) {
	t.Parallel()

	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.URL.Hostname() != "chatgpt.com" {
			t.Fatalf("hostname = %q, want chatgpt.com", req.URL.Hostname())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	}))

	client := NewUtlsHTTPClient(ctx, nil, nil, 0)
	resp, err := client.Get("https://chatgpt.com/backend-api/codex/responses")
	if err != nil {
		t.Fatalf("client.Get returned error: %v", err)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close returned error: %v", errClose)
	}
	if !called {
		t.Fatal("expected context RoundTripper to handle protected host request")
	}
}

type blockingUtlsDialer struct {
	started chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (d blockingUtlsDialer) Dial(network, addr string) (net.Conn, error) {
	d.once.Do(func() { close(d.started) })
	<-d.release
	return nil, errors.New("released blocking dial")
}

func (d blockingUtlsDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.once.Do(func() { close(d.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.release:
		return nil, errors.New("released blocking dial")
	}
}

func TestUtlsRoundTripperCancelsConnectionSetupWhenRequestContextEnds(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	rt := newUtlsRoundTripper("")
	rt.dialer = blockingUtlsDialer{started: started, release: release, once: &sync.Once{}}
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	if errReq != nil {
		t.Fatalf("NewRequestWithContext returned error: %v", errReq)
	}

	done := make(chan error, 1)
	go func() {
		_, errRoundTrip := rt.RoundTrip(req)
		done <- errRoundTrip
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("utls dial did not start")
	}
	select {
	case errRoundTrip := <-done:
		if !errors.Is(errRoundTrip, context.DeadlineExceeded) {
			t.Fatalf("RoundTrip error = %v, want context deadline exceeded", errRoundTrip)
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTrip did not return after request context deadline")
	}
}

type stalledUtlsHandshakeDialer struct {
	started chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (d stalledUtlsHandshakeDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d stalledUtlsHandshakeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	clientConn, serverConn := net.Pipe()
	d.once.Do(func() { close(d.started) })
	go func() {
		select {
		case <-ctx.Done():
		case <-d.release:
		}
		_ = serverConn.Close()
	}()
	return clientConn, nil
}

func TestUtlsRoundTripperCancelsConnectionSetupWhenTLSHandshakeStalls(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	rt := newUtlsRoundTripper("")
	rt.dialer = stalledUtlsHandshakeDialer{started: started, release: release, once: &sync.Once{}}
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	if errReq != nil {
		t.Fatalf("NewRequestWithContext returned error: %v", errReq)
	}

	done := make(chan error, 1)
	go func() {
		_, errRoundTrip := rt.RoundTrip(req)
		done <- errRoundTrip
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("utls dial did not start")
	}
	select {
	case errRoundTrip := <-done:
		if !errors.Is(errRoundTrip, context.DeadlineExceeded) {
			t.Fatalf("RoundTrip error = %v, want context deadline exceeded", errRoundTrip)
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTrip did not return after request context deadline")
	}
}

func TestUtlsRoundTripperCancelsSameHostWaitWhenRequestContextEnds(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	rt := newUtlsRoundTripper("")
	rt.dialer = blockingUtlsDialer{started: started, release: release, once: &sync.Once{}}

	firstReq, errReq := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	if errReq != nil {
		t.Fatalf("NewRequestWithContext returned error: %v", errReq)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, errRoundTrip := rt.RoundTrip(firstReq)
		firstDone <- errRoundTrip
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first utls dial did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	waitingReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/codex/second", nil)
	if errReq != nil {
		t.Fatalf("NewRequestWithContext returned error: %v", errReq)
	}

	waitingDone := make(chan error, 1)
	go func() {
		_, errRoundTrip := rt.RoundTrip(waitingReq)
		waitingDone <- errRoundTrip
	}()

	select {
	case errRoundTrip := <-waitingDone:
		if !errors.Is(errRoundTrip, context.DeadlineExceeded) {
			t.Fatalf("RoundTrip error = %v, want context deadline exceeded", errRoundTrip)
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTrip did not return while waiting on same-host connection")
	}

	close(release)
	<-firstDone
}

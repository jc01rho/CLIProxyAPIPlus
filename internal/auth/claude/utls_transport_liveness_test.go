package claude

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

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
	rt := &utlsRoundTripper{
		connections: make(map[string]*http2.ClientConn),
		pending:     make(map[string]chan struct{}),
		dialer:      blockingUtlsDialer{started: started, release: release, once: &sync.Once{}},
	}
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/messages", nil)
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

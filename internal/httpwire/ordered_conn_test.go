package httpwire

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestOrderedRequestConnReordersKeepAliveRequestsWithoutChangingBodies(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	t.Cleanup(func() {
		if errClose := client.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close client connection: %v", errClose)
		}
		if errClose := server.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})

	conn := NewOrderedRequestConn(client, func(method, target string) []string {
		if method == "POST" && target == "/v1/messages?beta=true" {
			return []string{"Accept", "Authorization", "Content-Type", "User-Agent", "Connection", "Host", "Accept-Encoding", "Content-Length"}
		}
		return []string{"Accept", "Host", "Connection"}
	})

	firstInput := "POST /v1/messages?beta=true HTTP/1.1\r\nHost: api.anthropic.com\r\nUser-Agent: claude-cli/2.1.220 (external, cli)\r\nContent-Length: 7\r\nAccept: application/json\r\nX-Unknown: keep\r\nAuthorization: Bearer placeholder\r\nContent-Type: application/json\r\nConnection: keep-alive\r\nAccept-Encoding: gzip, deflate, br, zstd\r\n\r\n{\"a\":1}"
	secondInput := "GET /api/oauth/profile HTTP/1.1\r\nConnection: close\r\nHost: api.anthropic.com\r\nAccept: application/json\r\n\r\n"
	want := "POST /v1/messages?beta=true HTTP/1.1\r\nAccept: application/json\r\nAuthorization: Bearer placeholder\r\nContent-Type: application/json\r\nUser-Agent: claude-cli/2.1.220 (external, cli)\r\nConnection: keep-alive\r\nHost: api.anthropic.com\r\nAccept-Encoding: gzip, deflate, br, zstd\r\nContent-Length: 7\r\nX-Unknown: keep\r\n\r\n{\"a\":1}GET /api/oauth/profile HTTP/1.1\r\nAccept: application/json\r\nHost: api.anthropic.com\r\nConnection: close\r\n\r\n"

	readDone := make(chan []byte, 1)
	go func() {
		if errDeadline := server.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
			readDone <- nil
			return
		}
		got := make([]byte, len(want))
		if _, errRead := io.ReadFull(server, got); errRead != nil {
			readDone <- nil
			return
		}
		readDone <- got
	}()

	parts := [][]byte{
		[]byte(firstInput[:29]),
		[]byte(firstInput[29 : len(firstInput)-3]),
		[]byte(firstInput[len(firstInput)-3:] + secondInput[:17]),
		[]byte(secondInput[17:]),
	}
	for _, part := range parts {
		written, errWrite := conn.Write(part)
		if errWrite != nil {
			t.Fatalf("write request bytes: %v", errWrite)
		}
		if written != len(part) {
			t.Fatalf("write length = %d, want %d", written, len(part))
		}
	}

	select {
	case got := <-readDone:
		if !bytes.Equal(got, []byte(want)) {
			t.Fatalf("wire bytes differ\n got: %q\nwant: %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading ordered request bytes")
	}
}

func TestOrderedRequestConnPreservesChunkedBody(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	conn := NewOrderedRequestConn(client, func(_, _ string) []string { return []string{"Host", "Transfer-Encoding"} })
	input := []byte("POST /upload HTTP/1.1\r\nTransfer-Encoding: chunked\r\nHost: example.com\r\n\r\n4\r\ntest\r\n0\r\n\r\n")
	want := []byte("POST /upload HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n4\r\ntest\r\n0\r\n\r\n")

	readDone := make(chan []byte, 1)
	go func() {
		got := make([]byte, len(want))
		_, _ = io.ReadFull(server, got)
		readDone <- got
	}()
	if _, errWrite := conn.Write(input); errWrite != nil {
		t.Fatal(errWrite)
	}
	if got := <-readDone; !bytes.Equal(got, want) {
		t.Fatalf("chunked wire bytes differ\n got: %q\nwant: %q", got, want)
	}
}

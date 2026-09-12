package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// devinStreamFrames reads Connect enveloped frames from a live response body
// and hands each one to fn as soon as it arrives.
//
// The upstream delivers the answer progressively, so buffering the whole body
// before replaying it collapses every token into one burst: clients then
// measure an inflated throughput and a time-to-first-token that includes the
// entire generation.
func devinStreamFrames(r io.Reader, fn func(frame []byte, isTrailer bool) error) error {
	br := bufio.NewReaderSize(r, 32<<10)
	hdr := make([]byte, 5)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		flag := hdr[0]
		size := binary.BigEndian.Uint32(hdr[1:5])
		if size > devinMaxNonStreamBytes {
			return fmt.Errorf("devin: frame too large: %d", size)
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(br, payload); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return fmt.Errorf("devin: truncated connect frame")
			}
			return err
		}
		if err := fn(payload, flag == 0x02); err != nil {
			return err
		}
	}
}

// devinOpenStream issues the upstream request and returns the live response so
// frames can be consumed as they arrive.
func (e *DevinExecutor) devinOpenStream(ctx context.Context, auth *cliproxyauth.Auth, model, url, token string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", devinConnectProtoContentType)
	req.Header.Set("connect-protocol-version", devinConnectProtocolVersion)
	req.Header.Set("authorization", devinAuthHeader(token))
	req.Header.Set("accept", "*/*")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		devinLogUpstreamFailure(ctx, auth, model, resp.StatusCode, body, respBody)
		return nil, fmt.Errorf("devin: upstream status %d: %s", resp.StatusCode, truncateDevinErr(respBody))
	}
	return resp, nil
}

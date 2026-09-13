package executor

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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
		// Connect flags: 0x01 marks a gzip payload, 0x02 the trailer.
		if flag&devinConnectCompressedFlag != 0 {
			decoded, errGz := devinGunzip(payload)
			if errGz != nil {
				return fmt.Errorf("devin: gunzip frame: %w", errGz)
			}
			payload = decoded
		}
		if err := fn(payload, flag&devinConnectEndStreamFlag != 0); err != nil {
			return err
		}
	}
}

// devinOpenStream issues the upstream request and returns the live response so
// frames can be consumed as they arrive.
func (e *DevinExecutor) devinOpenStream(ctx context.Context, auth *cliproxyauth.Auth, model, url, token string, body []byte) (*http.Response, error) {
	framed, errGz := devinGzipFrame(body)
	if errGz != nil {
		return nil, errGz
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(framed))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", devinConnectProtoContentType)
	req.Header.Set("connect-protocol-version", devinConnectProtocolVersion)
	req.Header.Set("authorization", devinAuthHeader(token))
	req.Header.Set("accept", "*/*")
	req.Header.Set("connect-content-encoding", "gzip")
	req.Header.Set("connect-accept-encoding", "gzip")
	req.Header.Set("accept-encoding", "identity")
	resp, err := e.devinHTTPClient(ctx, auth).Do(req)
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

// Connect envelope flags.
const (
	devinConnectCompressedFlag = 0x01
	devinConnectEndStreamFlag  = 0x02
)

// devinGunzip decompresses a gzip frame payload.
func devinGunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(io.LimitReader(zr, devinMaxNonStreamBytes))
}

// devinGzipFrame re-frames an uncompressed Connect envelope as a gzip frame.
//
// The native client compresses every GetChatMessage body and advertises it via
// connect-content-encoding. Sending large histories uncompressed is a known
// trigger for opaque invalid_argument trailers from the backend.
func devinGzipFrame(envelope []byte) ([]byte, error) {
	if len(envelope) < 5 {
		return envelope, nil
	}
	payload := envelope[5:]
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	gz := buf.Bytes()
	out := make([]byte, 0, 5+len(gz))
	out = append(out, devinConnectCompressedFlag)
	var ln [4]byte
	binary.BigEndian.PutUint32(ln[:], uint32(len(gz)))
	out = append(out, ln[:]...)
	return append(out, gz...), nil
}

// devinTrailerMessage extracts a readable message from a Connect trailer.
func devinTrailerMessage(trailer []byte) string {
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(trailer, &doc); err != nil {
		return truncateDevinErr(trailer)
	}
	if doc.Error.Code == "" && doc.Error.Message == "" {
		return truncateDevinErr(trailer)
	}
	return strings.TrimSpace(doc.Error.Code + ": " + doc.Error.Message)
}

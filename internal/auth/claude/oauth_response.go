package claude

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/lzw"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

func readClaudeOAuthResponseBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("read Claude OAuth response: body is nil")
	}
	encoded, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, errRead
	}
	encoding := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Encoding"), ",")[0]))
	if encoding == "" || encoding == "identity" {
		return encoded, nil
	}

	var reader io.ReadCloser
	switch encoding {
	case "gzip":
		gzipReader, errGzip := gzip.NewReader(bytes.NewReader(encoded))
		if errGzip != nil {
			return nil, fmt.Errorf("decode Claude OAuth gzip response: %w", errGzip)
		}
		reader = gzipReader
	case "deflate":
		zlibReader, errZlib := zlib.NewReader(bytes.NewReader(encoded))
		if errZlib == nil {
			reader = zlibReader
		} else {
			reader = flate.NewReader(bytes.NewReader(encoded))
		}
	case "br":
		reader = io.NopCloser(brotli.NewReader(bytes.NewReader(encoded)))
	case "compress":
		reader = lzw.NewReader(bytes.NewReader(encoded), lzw.MSB, 8)
	default:
		return nil, fmt.Errorf("decode Claude OAuth response: unsupported content encoding %q", encoding)
	}
	decoded, errDecoded := io.ReadAll(reader)
	if errDecoded != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("decode Claude OAuth %s response: %w", encoding, errDecoded)
	}
	if errClose := reader.Close(); errClose != nil {
		return nil, fmt.Errorf("close Claude OAuth %s decoder: %w", encoding, errClose)
	}
	return decoded, nil
}

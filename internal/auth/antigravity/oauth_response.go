package antigravity

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

const maxAntigravityOAuthResponseSize = 1 << 20

func readAntigravityOAuthResponseBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("read antigravity OAuth response: body is nil")
	}
	encoded, errRead := readAntigravityOAuthBody(resp.Body)
	if errRead != nil {
		return nil, errRead
	}
	encodings := strings.Split(strings.Join(resp.Header.Values("Content-Encoding"), ","), ",")
	for index := len(encodings) - 1; index >= 0; index-- {
		encoding := strings.ToLower(strings.TrimSpace(encodings[index]))
		if encoding == "" || encoding == "identity" {
			continue
		}
		var errDecode error
		encoded, errDecode = decodeAntigravityOAuthEncoding(encoded, encoding)
		if errDecode != nil {
			return nil, errDecode
		}
	}
	return encoded, nil
}

func decodeAntigravityOAuthEncoding(encoded []byte, encoding string) ([]byte, error) {
	var reader io.ReadCloser
	switch encoding {
	case "gzip":
		gzipReader, errGzip := gzip.NewReader(bytes.NewReader(encoded))
		if errGzip != nil {
			return nil, fmt.Errorf("decode antigravity OAuth gzip response: %w", errGzip)
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
	default:
		return nil, fmt.Errorf("decode antigravity OAuth response: unsupported content encoding %q", encoding)
	}
	decoded, errDecoded := readAntigravityOAuthBody(reader)
	if errDecoded != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("decode antigravity OAuth %s response: %w", encoding, errDecoded)
	}
	if errClose := reader.Close(); errClose != nil {
		return nil, fmt.Errorf("close antigravity OAuth %s decoder: %w", encoding, errClose)
	}
	return decoded, nil
}

func readAntigravityOAuthBody(reader io.Reader) ([]byte, error) {
	body, errRead := io.ReadAll(io.LimitReader(reader, maxAntigravityOAuthResponseSize+1))
	if errRead != nil {
		return nil, errRead
	}
	if len(body) > maxAntigravityOAuthResponseSize {
		return nil, fmt.Errorf("antigravity OAuth response exceeds %d bytes", maxAntigravityOAuthResponseSize)
	}
	return body, nil
}

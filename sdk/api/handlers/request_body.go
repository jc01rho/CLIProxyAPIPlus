package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

const (
	// MaxEncodedRequestBodyBytes bounds compressed or identity request bodies
	// read by SDK handlers before provider-specific parsing.
	MaxEncodedRequestBodyBytes int64 = 32 << 20 // 32 MiB
	// MaxDecodedRequestBodyBytes bounds decoded request bodies after applying
	// supported Content-Encoding values.
	MaxDecodedRequestBodyBytes int64 = 32 << 20 // 32 MiB
)

var ErrRequestBodyTooLarge = errors.New("request body too large")

// RequestBodyErrorStatus maps oversized request bodies to HTTP 413 while
// preserving the existing HTTP 400 response for malformed or unreadable bodies.
func RequestBodyErrorStatus(err error) int {
	if errors.Is(err, ErrRequestBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// ReadRequestBody reads the incoming request body and decodes supported
// Content-Encoding values before handlers inspect JSON fields.
func ReadRequestBody(c *gin.Context) ([]byte, error) {
	raw, err := readEncodedRequestBody(c)
	if err != nil {
		return nil, err
	}

	encoding := ""
	if c != nil && c.Request != nil {
		encoding = strings.TrimSpace(c.Request.Header.Get("Content-Encoding"))
	}
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw, nil
	}

	decoded, err := decodeRequestBody(raw, encoding)
	if err != nil {
		if json.Valid(raw) {
			return raw, nil
		}
		return nil, err
	}
	return decoded, nil
}

func readEncodedRequestBody(c *gin.Context) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, MaxEncodedRequestBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > MaxEncodedRequestBodyBytes {
		return nil, fmt.Errorf("encoded request body exceeds %d bytes: %w", MaxEncodedRequestBodyBytes, ErrRequestBodyTooLarge)
	}
	return raw, nil
}

func decodeRequestBody(raw []byte, encoding string) ([]byte, error) {
	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, err := decodeZstdRequestBody(body)
			if err != nil {
				return nil, err
			}
			body = decoded
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", enc)
		}
	}
	return body, nil
}

func decodeZstdRequestBody(raw []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
	}
	defer decoder.Close()

	decoded, err := io.ReadAll(io.LimitReader(decoder, MaxDecodedRequestBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	if int64(len(decoded)) > MaxDecodedRequestBodyBytes {
		return nil, fmt.Errorf("decoded request body exceeds %d bytes: %w", MaxDecodedRequestBodyBytes, ErrRequestBodyTooLarge)
	}
	return decoded, nil
}

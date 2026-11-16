package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
)

// MaxRequestBodyBytesContextKey stores the configured request body size limit (in bytes) on the Gin context.
// A positive value overrides the SDK fallback limits below, including values larger than the fallback.
const MaxRequestBodyBytesContextKey = "maxRequestBodyBytes"

// Fallback limits applied when no positive limit is configured on the Gin context.
const (
	// MaxEncodedRequestBodyBytes bounds compressed or identity request bodies
	// read by SDK handlers before provider-specific parsing.
	MaxEncodedRequestBodyBytes int64 = 32 << 20 // 32 MiB
	// MaxDecodedRequestBodyBytes bounds decoded request bodies after applying
	// supported Content-Encoding values.
	MaxDecodedRequestBodyBytes int64 = 32 << 20 // 32 MiB
)

var ErrRequestBodyTooLarge = errors.New("request body too large")

// RequestBodyErrorStatus maps oversized request bodies (ErrRequestBodyTooLarge or
// *http.MaxBytesError) to HTTP 413 while preserving the existing HTTP 400 response
// for malformed or unreadable bodies.
func RequestBodyErrorStatus(err error) int {
	if errors.Is(err, ErrRequestBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest)
}

// requestBodyTooLargeError satisfies both errors.Is(err, ErrRequestBodyTooLarge)
// and errors.As(err, **http.MaxBytesError) so fork and upstream callers agree.
type requestBodyTooLargeError struct {
	stage string
	limit int64
}

func (e *requestBodyTooLargeError) Error() string {
	return fmt.Sprintf("%s request body exceeds %d bytes: %v", e.stage, e.limit, ErrRequestBodyTooLarge)
}

func (e *requestBodyTooLargeError) Unwrap() []error {
	return []error{ErrRequestBodyTooLarge, &http.MaxBytesError{Limit: e.limit}}
}

func isRequestBodyTooLarge(err error) bool {
	if errors.Is(err, ErrRequestBodyTooLarge) {
		return true
	}
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

// requestBodyLimits returns the encoded and decoded body limits for c. A positive
// configured limit (set by the server middleware) overrides both fallbacks; when
// unset, standalone SDK handlers keep the 32 MiB fallback bounds.
func requestBodyLimits(c *gin.Context) (int64, int64) {
	if c != nil {
		if configured := c.GetInt64(MaxRequestBodyBytesContextKey); configured > 0 {
			return configured, configured
		}
	}
	return MaxEncodedRequestBodyBytes, MaxDecodedRequestBodyBytes
}

// ReadRequestBody reads the incoming request body and decodes supported
// Content-Encoding values before handlers inspect JSON fields.
func ReadRequestBody(c *gin.Context) ([]byte, error) {
	encodedLimit, decodedLimit := requestBodyLimits(c)
	raw, err := readEncodedRequestBody(c, encodedLimit)
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

	decoded, err := decodeRequestBody(raw, encoding, decodedLimit)
	if err != nil {
		if isRequestBodyTooLarge(err) {
			return nil, err
		}
		if json.Valid(raw) {
			return raw, nil
		}
		return nil, err
	}
	return decoded, nil
}

func readEncodedRequestBody(c *gin.Context, maxBytes int64) ([]byte, error) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		// Mirror gin's GetRawData contract for a missing body.
		return nil, errors.New("cannot read nil body")
	}
	raw, err := io.ReadAll(limitRequestBodyReader(c.Request.Body, maxBytes))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, &requestBodyTooLargeError{stage: "encoded", limit: maxBytesErr.Limit}
		}
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, &requestBodyTooLargeError{stage: "encoded", limit: maxBytes}
	}
	return raw, nil
}

// limitRequestBodyReader caps reads at maxBytes+1 so overflow is detectable,
// without overflowing when maxBytes is math.MaxInt64.
func limitRequestBodyReader(reader io.Reader, maxBytes int64) io.Reader {
	if maxBytes > 0 && maxBytes < math.MaxInt64 {
		return io.LimitReader(reader, maxBytes+1)
	}
	return reader
}

func decodeRequestBody(raw []byte, encoding string, maxBytes int64) ([]byte, error) {
	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, err := decodeZstdRequestBody(body, maxBytes)
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

func decodeZstdRequestBody(raw []byte, maxBytes int64) ([]byte, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
	}
	defer decoder.Close()

	decoded, err := io.ReadAll(limitRequestBodyReader(decoder, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	if maxBytes > 0 && int64(len(decoded)) > maxBytes {
		return nil, &requestBodyTooLargeError{stage: "decoded", limit: maxBytes}
	}
	return decoded, nil
}

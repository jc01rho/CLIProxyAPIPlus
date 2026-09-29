package executor

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/tidwall/gjson"
)

type nativeResponsesError struct {
	statusErr
	requestScoped bool
}

func (e nativeResponsesError) IsRequestScoped() bool { return e.requestScoped }

// Keep the nested failure contract without retaining echoed tools or input.
// The real HTTP status is authoritative; embedded status is used for HTTP 2xx.
func newNativeResponsesError(httpStatus int, headers http.Header, body []byte) error {
	root := gjson.ParseBytes(body)
	detail := root.Get("response.error")
	if !detail.IsObject() {
		detail = root.Get("error")
	}
	if !detail.IsObject() {
		detail = root
	}
	status := httpStatus
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
		hint := detail.Get("http_status")
		if hint.Type == gjson.Number && hint.Float() == float64(hint.Int()) && hint.Int() >= 400 && hint.Int() <= 599 {
			status = int(hint.Int())
		}
	}
	fields := map[string]any{"http_status": status}
	for _, key := range []string{"type", "code", "message", "request_id"} {
		if value := detail.Get(key); value.Type == gjson.String && value.String() != "" {
			fields[key] = value.String()
		}
	}
	if _, ok := fields["message"]; !ok {
		fields["message"] = "upstream Responses request failed"
	}
	if _, ok := fields["request_id"]; !ok {
		if id := root.Get("request_id"); id.Type == gjson.String && id.String() != "" {
			fields["request_id"] = id.String()
		} else if id := headers.Get("X-Request-Id"); id != "" {
			fields["request_id"] = id
		}
	}
	retryable := detail.Get("retryable")
	if retryable.Type == gjson.True || retryable.Type == gjson.False {
		fields["retryable"] = retryable.Bool()
	}
	var delay *time.Duration
	// An explicit false forbids retrying unchanged on this route. Request-scoped
	// errors can still enter the fork's separate configured model fallback.
	if retryable.Type != gjson.False {
		if raw := strings.TrimSpace(headers.Get("Retry-After")); raw != "" {
			if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
				delay = nativeResponsesRetryDelay(seconds)
			} else if deadline, err := http.ParseTime(raw); err == nil {
				delay = nativeResponsesRetryDelay(time.Until(deadline).Seconds())
			}
		}
		if delay == nil {
			if hint := detail.Get("retry_after"); hint.Type == gjson.Number {
				delay = nativeResponsesRetryDelay(hint.Float())
			}
		}
	}
	if delay != nil {
		fields["retry_after"] = delay.Seconds()
	}
	encoded, err := json.Marshal(map[string]any{"error": fields})
	if err != nil {
		return err
	}
	result := nativeResponsesError{statusErr: statusErr{code: status, msg: string(encoded), retryAfter: delay}}
	result.requestScoped = retryable.Type == gjson.False || clienterror.IsRequestFault(status, result.statusErr)
	return result
}

func nativeResponsesRetryDelay(seconds float64) *time.Duration {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return nil
	}
	d := time.Duration(seconds * float64(time.Second))
	if d <= 0 {
		return nil
	}
	return &d
}

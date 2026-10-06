package executor

import (
	"net/http"
	"testing"
	"time"
)

func TestClassifyClaudeUpstreamErrorReadsBodyRetryAfter(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		headers http.Header
		body    string
		want    time.Duration
	}{
		{"429 body hint", http.StatusTooManyRequests, nil, `{"type":"error","error":{"type":"rate_limit_error","retryable":true,"retry_after":20}}`, 20 * time.Second},
		{"429 retryable false is ignored", http.StatusTooManyRequests, nil, `{"error":{"retry_after":20,"retryable":false}}`, 0},
		{"429 non-positive is ignored", http.StatusTooManyRequests, nil, `{"error":{"retry_after":0}}`, 0},
		{"429 without hint", http.StatusTooManyRequests, nil, `{"error":{"type":"rate_limit_error"}}`, 0},
		{"non-429 body hint is ignored", http.StatusServiceUnavailable, nil, `{"error":{"retry_after":20}}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyClaudeUpstreamError(tt.status, tt.headers, []byte(tt.body))
			hinted, ok := err.(interface{ RetryAfter() *time.Duration })
			if !ok {
				t.Fatalf("error %T does not expose RetryAfter", err)
			}
			got := hinted.RetryAfter()
			if tt.want == 0 {
				if got != nil {
					t.Fatalf("retry-after = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != tt.want {
				t.Fatalf("retry-after = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClassifyClaudeUpstreamErrorHeaderWinsOverBodyHint(t *testing.T) {
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, http.Header{"Retry-After": {"3"}}, []byte(`{"error":{"retry_after":600}}`))
	got := err.(interface{ RetryAfter() *time.Duration }).RetryAfter()
	if got == nil || *got < 3*time.Second || *got >= 600*time.Second {
		t.Fatalf("retry-after = %v, want the header-derived value (>=3s, far below the 600s body hint)", got)
	}
}

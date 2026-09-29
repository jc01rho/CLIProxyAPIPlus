package executor

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNativeResponsesFailureMetadata(t *testing.T) {
	rootID := newNativeResponsesError(200, http.Header{}, []byte(`{"request_id":"root-id","error":{"http_status":503,"message":"failed"}}`))
	if gjson.Get(rootID.Error(), "error.request_id").String() != "root-id" {
		t.Fatalf("lost root request id: %s", rootID)
	}
	for _, tc := range []struct {
		name, body, retryHeader string
		httpStatus, wantStatus  int
		wantDelay               time.Duration
		requestScoped           bool
	}{
		{"nested request error", `{"response":{"error":{"type":"invalid_request_error","code":"upstream_invalid_request","message":"bad argument","request_id":"r1","http_status":400,"retryable":false},"tools":["PRIVATE"]}}`, "", 200, 400, 0, true},
		{"nested transient", `{"response":{"error":{"type":"overloaded_error","code":"upstream_empty_response","message":"empty","http_status":503,"retry_after":5,"retryable":true},"tools":["PRIVATE"]}}`, "", 200, 503, 5 * time.Second, false},
		{"header precedence", `{"error":{"http_status":400,"retry_after":2}}`, "7", 503, 503, 7 * time.Second, false},
		{"false suppresses hint", `{"error":{"http_status":503,"retryable":false,"retry_after":5}}`, "7", 200, 503, 0, true},
		{"invalid numeric", `{"error":{"http_status":200,"retry_after":-5}}`, "garbage", 200, 502, 0, false},
		{"overflow", `{"error":{"http_status":503.2,"retry_after":1e100}}`, "NaN", 200, 502, 0, false},
		{"malformed body", `PRIVATE not json`, "", 200, 502, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newNativeResponsesError(tc.httpStatus, http.Header{"Retry-After": []string{tc.retryHeader}}, []byte(tc.body))
			var coded interface{ StatusCode() int }
			if !errors.As(err, &coded) || coded.StatusCode() != tc.wantStatus {
				t.Fatalf("status error: %v", err)
			}
			var retry interface{ RetryAfter() *time.Duration }
			if !errors.As(err, &retry) {
				t.Fatal("missing retry contract")
			}
			if delay := retry.RetryAfter(); (delay == nil && tc.wantDelay != 0) || (delay != nil && *delay != tc.wantDelay) {
				t.Fatalf("delay = %v, want %v", delay, tc.wantDelay)
			}
			var scoped cliproxyexecutor.RequestScopedError
			if !errors.As(err, &scoped) || scoped.IsRequestScoped() != tc.requestScoped {
				t.Fatalf("request scope: %v", err)
			}
			if strings.Contains(err.Error(), "PRIVATE") || !gjson.Valid(err.Error()) {
				t.Fatalf("unsafe error = %s", err)
			}
		})
	}
	for _, seconds := range []float64{-1, 0, math.Inf(1), math.NaN(), 1e100} {
		if nativeResponsesRetryDelay(seconds) != nil {
			t.Fatalf("accepted invalid retry hint %v", seconds)
		}
	}
}

func TestNativeResponsesBootstrapAndLateFailure(t *testing.T) {
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\"}}\n\n"
	emptyItem := "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m\",\"content\":[]}}\n\n"
	delta := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"text\"}\n\n"
	failure := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"failure\",\"http_status\":503,\"retry_after\":5},\"tools\":[\"PRIVATE\"]}}\n\n"
	complete := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n"
	tool := "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"fc\",\"name\":\"todo\",\"arguments\":\"\"}}\n\n"
	// Upstreams echo the full request instructions in created/in_progress, so a
	// large request exceeds the bootstrap cap before any output is produced.
	echo := strings.Repeat("x", 170<<10)
	bigCreated := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\",\"instructions\":\"" + echo + "\"}}\n\n"
	bigProgress := strings.Replace(bigCreated, "response.created", "response.in_progress", 1)
	for _, tc := range []struct {
		name, wire  string
		wantPayload bool
		wantError   bool
	}{
		{"early failure", created + emptyItem + failure, false, true},
		{"early EOF", created + emptyItem, false, true},
		{"late failure", created + emptyItem + delta + failure, true, true},
		{"whitespace is output", created + strings.Replace(delta, `"delta":"text"`, `"delta":" "`, 1) + failure, true, true},
		{"populated part commits", created + "data: {\"type\":\"response.content_part.added\",\"part\":{\"type\":\"output_text\",\"text\":\"text\"}}\n\n" + failure, true, true},
		{"tool commits", created + tool + failure, true, true},
		{"empty complete", created + complete, true, false},
		{"incomplete", created + strings.ReplaceAll(complete, "completed", "incomplete"), true, false},
		{"normal", created + emptyItem + delta + complete, true, false},
		{"bounded bootstrap releases stream", strings.Repeat(created, 4096), true, true},
		{"large request echo passes through", bigCreated + bigProgress + emptyItem + delta + complete, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := io.WriteString(w, tc.wire); err != nil && tc.name != "bounded bootstrap releases stream" {
					t.Error(err)
				}
			}))
			defer server.Close()
			e, auth := nativeResponsesExecutor(server.URL)
			stream, err := e.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "upstream", Payload: []byte(`{"input":"hi"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if err != nil {
				t.Fatal(err)
			}
			var payload strings.Builder
			var failure error
			for chunk := range stream.Chunks {
				payload.Write(chunk.Payload)
				if chunk.Err != nil {
					failure = chunk.Err
				}
			}
			if (payload.Len() > 0) != tc.wantPayload || (failure != nil) != tc.wantError {
				t.Fatalf("payload=%q err=%v", payload.String(), failure)
			}
			if failure != nil && strings.Contains(failure.Error(), "PRIVATE") {
				t.Fatal("echo leaked")
			}
			if tc.wantPayload && !strings.HasPrefix(payload.String(), "event: response.created\ndata:") {
				t.Fatalf("lost ordered SSE envelope: %s", payload.String())
			}
		})
	}
}

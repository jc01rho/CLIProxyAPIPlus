package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func Test_CommandCodeExecutorExecute_continues_on_pause_turn(t *testing.T) {
	var postCount int
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", commandCodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		postCount++
		switch postCount {
		case 1:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
				Body: io.NopCloser(strings.NewReader(
					"{\"type\":\"text-delta\",\"text\":\"part one\"}\n" +
						"{\"type\":\"finish\",\"finishReason\":\"pause_turn\",\"totalUsage\":{\"inputTokens\":10,\"outputTokens\":20}}\n",
				)),
			}, nil
		case 2:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
				Body: io.NopCloser(strings.NewReader(
					"{\"type\":\"text-delta\",\"text\":\"part two\"}\n" +
						"{\"type\":\"finish\",\"finishReason\":\"stop\",\"totalUsage\":{\"inputTokens\":5,\"outputTokens\":7}}\n",
				)),
			}, nil
		default:
			t.Fatalf("unexpected extra POST: %d", postCount)
			return nil, nil
		}
	}))

	executor := NewCommandCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "user_test"}}
	payload := []byte(`{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"hello"}]}`)
	response, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-pro",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if postCount != 2 {
		t.Fatalf("postCount = %d, want 2 (one continuation)", postCount)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != "part onepart two" {
		t.Fatalf("content = %q, want concatenated parts; payload=%s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop after final session", got)
	}
}

func Test_CommandCodeStreamAccumulator_IsPauseTurn(t *testing.T) {
	acc := newCommandCodeStreamAccumulator()
	acc.feed(decodeCommandCodeStreamEvent([]byte(`{"type":"finish","finishReason":"pause_turn"}`)))
	if !acc.IsPauseTurn() {
		t.Fatalf("IsPauseTurn() = false, want true for pause_turn finish")
	}
	if got := acc.stopReason; got != "stop" {
		t.Fatalf("stopReason = %q, want stop (pause_turn never surfaces downstream)", got)
	}
}

func Test_CommandCodeIsStreamErrorRetryable(t *testing.T) {
	tests := []struct {
		name        string
		isRetryable *bool
		status      int
		message     string
		want        bool
	}{
		{name: "explicit true", isRetryable: boolPtr(true), status: 500, message: "boom", want: true},
		{name: "explicit false with reported status overrides", isRetryable: boolPtr(false), status: 500, message: "boom", want: true},
		{name: "status 429 with no flag", status: 429, message: "rate limited", want: true},
		{name: "status 503 with no flag", status: 503, message: "unavailable", want: true},
		{name: "status 400 no flag", status: 400, message: "bad", want: false},
		{name: "no status no flag no marker", message: "generic", want: true},
		{name: "no status premium marker", message: "premium_credits_exhausted", want: false},
		{name: "no status model_not_in_plan marker", message: "model_not_in_plan", want: false},
		{name: "no status insufficient credits marker", message: "You have insufficient credits", want: false},
		{name: "no status explicit false", isRetryable: boolPtr(false), message: "boom", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandCodeIsStreamErrorRetryable(tt.isRetryable, tt.status, tt.message); got != tt.want {
				t.Fatalf("commandCodeIsStreamErrorRetryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

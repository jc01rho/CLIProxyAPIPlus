package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorRetriesHTTP200DeveloperRoleError(t *testing.T) {
	var roles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		roles = append(roles, gjson.GetBytes(body, "messages.0.role").String())
		w.Header().Set("Content-Type", "application/json")
		if len(roles) == 1 {
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	resp, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(roles) != 2 || roles[0] != "developer" || roles[1] != "system" {
		t.Fatalf("roles = %v, want [developer system]", roles)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "ok" {
		t.Fatalf("content = %q, want ok", got)
	}
}

func TestOpenAICompatExecutorRejectsHTTP200EmbeddedErrorWithBadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"upstream failed","type":"server_error"}}`))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	_, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err == nil {
		t.Fatal("Execute unexpectedly accepted HTTP 200 embedded error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status = %v, want %d", err, http.StatusBadGateway)
	}
}

func TestOpenAICompatExecutorSuccessfulRetryLearnsDeveloperRoleFallback(t *testing.T) {
	var roles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		roles = append(roles, gjson.GetBytes(body, "messages.0.role").String())
		w.Header().Set("Content-Type", "application/json")
		if len(roles) == 1 {
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
	for execution := 0; execution < 2; execution++ {
		if _, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), opts); err != nil {
			t.Fatalf("Execute %d error: %v", execution+1, err)
		}
	}
	if len(roles) != 3 || roles[0] != "developer" || roles[1] != "system" || roles[2] != "system" {
		t.Fatalf("roles = %v, want [developer system system]", roles)
	}
}

func TestOpenAICompatExecutorFailedRetryDoesNotPoisonDeveloperRoleCache(t *testing.T) {
	var roles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		roles = append(roles, gjson.GetBytes(body, "messages.0.role").String())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
	if _, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), opts); err == nil {
		t.Fatal("first Execute unexpectedly succeeded")
	}
	if _, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), opts); err == nil {
		t.Fatal("second Execute unexpectedly succeeded")
	}
	if len(roles) != 4 || roles[0] != "developer" || roles[1] != "system" || roles[2] != "developer" || roles[3] != "system" {
		t.Fatalf("roles = %v, want [developer system developer system]", roles)
	}
}

func TestOpenAICompatExecutorFailedHTTP200RetryDoesNotPoisonDeveloperRoleCache(t *testing.T) {
	var roles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		roles = append(roles, gjson.GetBytes(body, "messages.0.role").String())
		w.Header().Set("Content-Type", "application/json")
		if len(roles)%2 == 1 {
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":`))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
	for execution := 0; execution < 2; execution++ {
		_, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), opts)
		if err == nil {
			t.Fatalf("Execute %d unexpectedly succeeded", execution+1)
		}
		status, ok := err.(interface{ StatusCode() int })
		if !ok || status.StatusCode() != http.StatusBadGateway {
			t.Fatalf("Execute %d status = %v, want %d", execution+1, err, http.StatusBadGateway)
		}
	}
	if len(roles) != 4 || roles[0] != "developer" || roles[1] != "system" || roles[2] != "developer" || roles[3] != "system" {
		t.Fatalf("roles = %v, want [developer system developer system]", roles)
	}
}

func TestOpenAICompatExecutorRejectsUnusableDeveloperRoleRetryBodies(t *testing.T) {
	for name, response := range map[string]string{
		"empty":           "",
		"malformed":       `{"choices":`,
		"unrecognized":    `{"id":"chatcmpl-1"}`,
		"top-level error": `{"error":{"message":"upstream failed"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				if requests == 1 {
					_, _ = w.Write([]byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`))
					return
				}
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			executor, auth := newDeveloperRoleTestExecutor(server.URL)
			err := func() error {
				_, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
				return err
			}()
			if err == nil {
				t.Fatalf("Execute unexpectedly accepted retry response %q", response)
			}
			status, ok := err.(interface{ StatusCode() int })
			if !ok || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("status = %v, want %d", err, http.StatusBadGateway)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want 2", requests)
			}
		})
	}
}

func TestOpenAICompatExecutorRejectsHTTP204DeveloperRoleRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	_, err := executor.Execute(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err == nil {
		t.Fatal("Execute unexpectedly accepted HTTP 204 developer-role retry")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status = %v, want %d", err, http.StatusBadGateway)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestOpenAICompatExecutorEmbeddedRetryReturnsFinalHeaders(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			w.Header().Set("X-Attempt", "rejected")
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"unsupported role: developer\",\"type\":\"invalid_request_error\"}}\n\n"))
			return
		}
		w.Header().Set("X-Attempt", "retry")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	result, err := executor.ExecuteStream(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if got := result.Headers.Get("X-Attempt"); got != "retry" {
		t.Fatalf("X-Attempt = %q, want retry", got)
	}
	drainStreamChunks(t, result.Chunks)
}

func TestOpenAICompatExecutorRetriesRawJSONStreamDeveloperError(t *testing.T) {
	var roles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		roles = append(roles, gjson.GetBytes(body, "messages.0.role").String())
		if len(roles) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Attempt", "rejected")
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Attempt", "retry")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	result, err := executor.ExecuteStream(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if got := result.Headers.Get("X-Attempt"); got != "retry" {
		t.Fatalf("X-Attempt = %q, want retry", got)
	}
	drainStreamChunks(t, result.Chunks)
	if len(roles) != 2 || roles[0] != "developer" || roles[1] != "system" {
		t.Fatalf("roles = %v, want [developer system]", roles)
	}
}

func TestOpenAICompatExecutorCleanRetryStreamLearnsAfterCompletion(t *testing.T) {
	var roles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		roles = append(roles, gjson.GetBytes(body, "messages.0.role").String())
		w.Header().Set("Content-Type", "text/event-stream")
		if len(roles) == 1 {
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"unsupported role: developer\",\"type\":\"invalid_request_error\"}}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}
	for execution := 0; execution < 2; execution++ {
		result, err := executor.ExecuteStream(context.Background(), auth, developerRoleTestRequest(), opts)
		if err != nil {
			t.Fatalf("ExecuteStream %d error: %v", execution+1, err)
		}
		drainStreamChunks(t, result.Chunks)
	}
	if len(roles) != 3 || roles[0] != "developer" || roles[1] != "system" || roles[2] != "system" {
		t.Fatalf("roles = %v, want [developer system system]", roles)
	}
}

func TestOpenAICompatExecutorRetryStreamBareEOFDoesNotLearnDeveloperRoleFallback(t *testing.T) {
	var roles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		roles = append(roles, gjson.GetBytes(body, "messages.0.role").String())
		w.Header().Set("Content-Type", "text/event-stream")
		switch len(roles) {
		case 1, 3:
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"unsupported role: developer\",\"type\":\"invalid_request_error\"}}\n\n"))
		case 2:
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"truncated\"}}]}\n\n"))
		default:
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"complete\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}
	for execution := 0; execution < 2; execution++ {
		result, err := executor.ExecuteStream(context.Background(), auth, developerRoleTestRequest(), opts)
		if err != nil {
			t.Fatalf("ExecuteStream %d error: %v", execution+1, err)
		}
		drainStreamChunks(t, result.Chunks)
	}
	if len(roles) != 4 || roles[0] != "developer" || roles[1] != "system" || roles[2] != "developer" || roles[3] != "system" {
		t.Fatalf("roles = %v, want [developer system developer system]", roles)
	}
}

func TestOpenAICompatExecutorRetryContentThenErrorDoesNotPoisonCache(t *testing.T) {
	var roles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		roles = append(roles, gjson.GetBytes(body, "messages.0.role").String())
		w.Header().Set("Content-Type", "text/event-stream")
		switch len(roles) {
		case 1, 3:
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"unsupported role: developer\",\"type\":\"invalid_request_error\"}}\n\n"))
		default:
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"upstream failed\",\"type\":\"server_error\"}}\n\n"))
		}
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}
	for execution := 0; execution < 2; execution++ {
		result, err := executor.ExecuteStream(context.Background(), auth, developerRoleTestRequest(), opts)
		if err != nil {
			t.Fatalf("ExecuteStream %d error: %v", execution+1, err)
		}
		var terminal error
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				terminal = chunk.Err
			}
		}
		if terminal == nil {
			t.Fatalf("ExecuteStream %d missing terminal error", execution+1)
		}
	}
	if len(roles) != 4 || roles[0] != "developer" || roles[1] != "system" || roles[2] != "developer" || roles[3] != "system" {
		t.Fatalf("roles = %v, want [developer system developer system]", roles)
	}
}

func TestOpenAICompatExecutorGenericStructuredStreamErrorAfterContentIsTerminal(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"visible\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"rate limit\",\"type\":\"rate_limit_error\"}}\n\n"))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	result, err := executor.ExecuteStream(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var terminal error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			terminal = chunk.Err
		}
	}
	if terminal == nil || requests != 1 {
		t.Fatalf("terminal=%v requests=%d, want terminal error and one request", terminal, requests)
	}
	if status, ok := terminal.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("terminal status = %v, want %d", terminal, http.StatusBadGateway)
	}
}

func TestOpenAICompatExecutorRepeatedEmbeddedDeveloperErrorIsTerminal(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"unsupported role: developer\",\"type\":\"invalid_request_error\"}}\n\n"))
	}))
	defer server.Close()

	executor, auth := newDeveloperRoleTestExecutor(server.URL)
	result, err := executor.ExecuteStream(context.Background(), auth, developerRoleTestRequest(), cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var terminal error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			terminal = chunk.Err
		}
	}
	if terminal == nil || requests != 2 {
		t.Fatalf("terminal=%v requests=%d, want terminal error and two requests", terminal, requests)
	}
	if status, ok := terminal.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("terminal status = %v, want %d", terminal, http.StatusBadGateway)
	}
}

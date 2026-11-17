package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/requestmeta"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestManagerMarkResultLogsFailureRequestDetails(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-commandcode",
		Provider: "commandcode",
		Metadata: map[string]any{
			"type": "commandcode",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "higher-coding",
			cliproxyexecutor.RequestPathMetadataKey:    "/v1/chat/completions",
		},
	}
	ctx = contextWithRequestedModelAlias(ctx, opts, "higher-coding")
	requestmeta.RecordLatestUpstreamRequest(ctx, requestmeta.UpstreamRequestSummary{
		URL:      "https://api.commandcode.ai/alpha/generate",
		Method:   http.MethodPost,
		Provider: "commandcode",
		AuthID:   auth.ID,
	})

	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	mgr.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "commandcode",
		Model:    "claude-sonnet-4-5",
		Success:  false,
		Error: &Error{
			Code:       "BAD_REQUEST",
			Message:    "invalid request",
			HTTPStatus: http.StatusBadRequest,
		},
	})

	got := logBuf.String()
	for _, want := range []string{
		"request failed",
		"provider=commandcode",
		"model=claude-sonnet-4-5",
		"selected_model=claude-sonnet-4-5",
		"requested_model=higher-coding",
		"request_path=/v1/chat/completions",
		"upstream_url=",
		"endpoint=",
		"https://api.commandcode.ai/alpha/generate",
		"upstream_method=POST",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got: %s", want, got)
		}
	}
}

func TestManagerMarkResultLogsResolvedUpstreamModelWhenRequestedModelIsAlias(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-commandcode",
		Provider: "commandcode",
		Metadata: map[string]any{
			"type": "commandcode",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "higher-coding",
		},
	}
	ctx = contextWithRequestedModelAlias(ctx, opts, "higher-coding")
	requestmeta.RecordLatestUpstreamRequest(ctx, requestmeta.UpstreamRequestSummary{
		URL:      "https://api.commandcode.ai/alpha/generate",
		Method:   http.MethodPost,
		Provider: "commandcode",
		AuthID:   auth.ID,
		Model:    "nvidia/nemotron-3-ultra-550b-a55b",
	})

	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	mgr.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "commandcode",
		Model:    "higher-coding",
		Success:  false,
		Error: &Error{
			Code:       "BAD_REQUEST",
			Message:    "insufficient credits",
			HTTPStatus: http.StatusBadRequest,
		},
	})

	got := logBuf.String()
	for _, want := range []string{
		"request failed",
		"provider=commandcode",
		"model=higher-coding",
		"requested_model=higher-coding",
		"selected_model=nvidia/nemotron-3-ultra-550b-a55b",
		"upstream_model=nvidia/nemotron-3-ultra-550b-a55b",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got: %s", want, got)
		}
	}
}

func TestManagerMarkResultLogsFailedCredentialIdentity(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "claude-oauth-1",
		Provider: "claude",
		FileName: "/tmp/cliproxy/claude-user.json",
		Label:    "Claude Team Seat",
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindOAuth,
		},
		Metadata: map[string]any{
			"email": "user@example.com",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "claude",
		Model:    "claude-haiku-4-5-20251001",
		Success:  false,
		Error: &Error{
			Code:       "authentication_error",
			Message:    "Invalid authentication credentials",
			HTTPStatus: http.StatusUnauthorized,
		},
	})

	got := logBuf.String()
	for _, want := range []string{
		"request failed",
		"auth_id=claude-oauth-1",
		"auth_kind=oauth",
		"credential=user@example.com",
		"auth_file=claude-user.json",
		"auth_label=\"Claude Team Seat\"",
		"provider=claude",
		"model=claude-haiku-4-5-20251001",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got: %s", want, got)
		}
	}
}

func TestManagerMarkResultLogsDownstreamAPIKeyFromAuthorizationHeader(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-codex",
		Provider: "codex",
		Metadata: map[string]any{
			"email": "TE_tg_sp_devcenter@sparrow.im",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer sk-downstream-who-requested")
	ginCtx.Request = req
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	mgr.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			Code:       "unknown_parameter",
			Message:    "Unknown parameter: 'input[359].status'.",
			HTTPStatus: http.StatusBadRequest,
		},
	})

	got := logBuf.String()
	for _, want := range []string{
		"request failed",
		"provider=codex",
		"model=gpt-5.6-sol",
		"credential=TE_tg_sp_devcenter@sparrow.im",
		`downstream_api_key=sk-downstream-who-requested`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got: %s", want, got)
		}
	}
}

func TestManagerMarkResultLogsDownstreamAPIKeyFromUserAPIKey(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-codex",
		Provider: "codex",
		Metadata: map[string]any{
			"email": "TE_tg_sp_devcenter@sparrow.im",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginCtx.Set("userApiKey", "sk-query-key-home-user")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	mgr.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			Code:       "unknown_parameter",
			Message:    "Unknown parameter: 'input[359].status'.",
			HTTPStatus: http.StatusBadRequest,
		},
	})

	got := logBuf.String()
	if !strings.Contains(got, "downstream_api_key=sk-query-key-home-user") {
		t.Fatalf("expected userApiKey fallback in request failed log, got: %s", got)
	}
}

// An upstream context_length_exceeded failure is only diagnosable when the log
// carries the preflight input estimate that caused it.
func TestManagerMarkResultLogsEstimatedInputTokens(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	auth := &Auth{ID: "rainby-raid-auth", Provider: "openai-compatibility"}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "openai-compatibility",
		Model:    "ollama-deepseek-v4.1-flash",
		Success:  false,
		Error: &Error{
			Code:       "request_scoped",
			Message:    `{"error":{"message":"Request failed or timed out (upstream: context_length_exceeded)"}}`,
			HTTPStatus: http.StatusBadRequest,
		},
		Options: cliproxyexecutor.Options{
			Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: 131072},
		},
	})

	got := logBuf.String()
	if !strings.Contains(got, "request failed") {
		t.Fatalf("expected failure log, got: %s", got)
	}
	if !strings.Contains(got, "estimated_input_tokens=131072") {
		t.Fatalf("expected estimated_input_tokens=131072 in failure log, got: %s", got)
	}
}

func TestManagerMarkResultOmitsEstimatedInputTokensWhenUnknown(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	auth := &Auth{ID: "rainby-raid-auth", Provider: "openai-compatibility"}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	logBuf, restoreLogger := captureStandardLogger(t)
	defer restoreLogger()

	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "openai-compatibility",
		Model:    "ollama-deepseek-v4.1-flash",
		Success:  false,
		Error: &Error{
			Code:       "request_scoped",
			Message:    "request failed",
			HTTPStatus: http.StatusBadRequest,
		},
	})

	got := logBuf.String()
	if !strings.Contains(got, "request failed") {
		t.Fatalf("expected failure log, got: %s", got)
	}
	if strings.Contains(got, "estimated_input_tokens") {
		t.Fatalf("expected no estimated_input_tokens field, got: %s", got)
	}
}

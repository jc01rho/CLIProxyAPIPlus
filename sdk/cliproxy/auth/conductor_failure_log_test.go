package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/requestmeta"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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

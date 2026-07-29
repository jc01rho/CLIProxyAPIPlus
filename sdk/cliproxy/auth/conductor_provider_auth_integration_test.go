package auth_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type providerAuthIntegrationExecutor struct {
	id string
}

func (e *providerAuthIntegrationExecutor) Identifier() string { return e.id }

func (e *providerAuthIntegrationExecutor) Execute(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"id":"chatcmpl-test","model":"` + req.Model + `"}`)}, nil
}

func (e *providerAuthIntegrationExecutor) ExecuteStream(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *providerAuthIntegrationExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *providerAuthIntegrationExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"total_tokens":0}`)}, nil
}

func (e *providerAuthIntegrationExecutor) HttpRequest(_ context.Context, _ *cliproxyauth.Auth, _ *http.Request) (*http.Response, error) {
	return nil, errors.New("not used by provider auth integration test")
}

func TestGinLoggerEndToEndIncludesSelectedProviderAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger := log.StandardLogger()
	previousOutput := logger.Out
	previousLevel := logger.Level
	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
	})

	const (
		model     = "gpt-5.6-sol"
		authID    = "auth-file-1.json"
		authLabel = "someone@example.com"
	)

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(&providerAuthIntegrationExecutor{id: "codex"})
	if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{ID: authID, Provider: "codex", Label: authLabel, Status: cliproxyauth.StatusActive}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	engine := gin.New()
	engine.Use(internallogging.GinLogrusLogger(&config.Config{}))
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		if _, err := manager.Execute(c.Request.Context(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"`+model+`"}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}

	logOutput := logBuffer.String()
	t.Logf("access log: %s", strings.TrimSpace(logOutput))
	want := model + " | codex:" + authLabel
	if !bytes.Contains([]byte(logOutput), []byte(want)) {
		t.Fatalf("expected end-to-end provider auth segment %q in log, got: %s", want, logOutput)
	}
}

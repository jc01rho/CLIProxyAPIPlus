package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestGetAuthFileModelsAppliesAuthExcludedModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "github-copilot-auth-models-test",
		Provider: "github-copilot",
		FileName: "/tmp/auths/github-copilot-jc01rho.json",
		Attributes: map[string]string{
			"excluded_models": "gemini-2.5-pro",
		},
	}
	registered, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(registered.ID, registered.Provider, []*registry.ModelInfo{
		{ID: "claude-haiku-4.5", Type: "github-copilot"},
		{ID: "gemini-2.5-pro", Type: "github-copilot"},
	})
	defer reg.UnregisterClient(registered.ID)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name=github-copilot-jc01rho.json", nil)

	h.GetAuthFileModels(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var payload struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	ids := make(map[string]bool)
	for _, model := range payload.Models {
		ids[model.ID] = true
	}
	if ids["gemini-2.5-pro"] {
		t.Fatalf("expected excluded model gemini-2.5-pro to be hidden, got response %s", rec.Body.String())
	}
	if !ids["claude-haiku-4.5"] {
		t.Fatalf("expected allowed model claude-haiku-4.5 to remain, got response %s", rec.Body.String())
	}
}

func TestGetAuthFileModelsAllowsOnlySupportedCopilotModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "github-copilot-suppressed-models-test",
		Provider: "github-copilot",
		FileName: "github-copilot-jc01rho.json",
	}
	registered, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(registered.ID, registered.Provider, []*registry.ModelInfo{
		{ID: "claude-haiku-4.5", Type: "github-copilot"},
		{ID: "gemini-2.5-pro", Type: "github-copilot"},
		{ID: "gemini-3-pro-preview", Type: "github-copilot"},
		{ID: "gemini-3.1-pro-preview", Type: "github-copilot"},
		{ID: "gemini-3-flash-preview", Type: "github-copilot"},
		{ID: "gpt-5.5", Type: "github-copilot"},
		{ID: "claude-sonnet-4.6", Type: "github-copilot"},
	})
	defer reg.UnregisterClient(registered.ID)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name=github-copilot-jc01rho.json", nil)

	h.GetAuthFileModels(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	for _, modelID := range []string{"gpt-5.5", "claude-sonnet-4.6"} {
		if containsModelID(got, modelID) {
			t.Fatalf("expected unsupported Copilot model %s to be hidden, got response %s", modelID, got)
		}
	}
	for _, modelID := range []string{
		"claude-haiku-4.5",
		"gemini-2.5-pro",
		"gemini-3-pro-preview",
		"gemini-3.1-pro-preview",
		"gemini-3-flash-preview",
	} {
		if !containsModelID(got, modelID) {
			t.Fatalf("expected allowed Copilot model %s to remain, got response %s", modelID, got)
		}
	}
}

func TestGetAuthFileModelsAppliesCopilotAllowlistWithoutMatchedAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	clientID := "github-copilot-unmatched-auth-test"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "github-copilot", []*registry.ModelInfo{
		{ID: "claude-haiku-4.5", Type: "github-copilot"},
		{ID: "gpt-5.5", Type: "github-copilot"},
	})
	defer reg.UnregisterClient(clientID)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+clientID, nil)

	h.GetAuthFileModels(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	if containsModelID(got, "gpt-5.5") {
		t.Fatalf("expected unsupported Copilot model to be hidden without matched auth, got response %s", got)
	}
	if !containsModelID(got, "claude-haiku-4.5") {
		t.Fatalf("expected allowed Copilot model to remain without matched auth, got response %s", got)
	}
}

func TestGetAuthFileModelsAppliesGlobalOAuthExcludedModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "github-copilot-global-excluded-test",
		Provider: "github-copilot",
		FileName: "github-copilot-global.json",
	}
	registered, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(registered.ID, registered.Provider, []*registry.ModelInfo{
		{ID: "claude-haiku-4.5", Type: "github-copilot"},
		{ID: "gemini-2.5-pro", Type: "github-copilot"},
	})
	defer reg.UnregisterClient(registered.ID)

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		AuthDir: t.TempDir(),
		OAuthExcludedModels: map[string][]string{
			"github-copilot": {"gemini-2.5-pro"},
		},
	}, manager)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name=github-copilot-global.json", nil)

	h.GetAuthFileModels(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); containsModelID(got, "gemini-2.5-pro") {
		t.Fatalf("expected global OAuth excluded model gemini-2.5-pro to be hidden, got response %s", got)
	}
}

func containsModelID(body, id string) bool {
	var payload struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return false
	}
	for _, model := range payload.Models {
		if model.ID == id {
			return true
		}
	}
	return false
}

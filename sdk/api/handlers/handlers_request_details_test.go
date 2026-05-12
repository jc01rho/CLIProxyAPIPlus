package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestGetRequestDetails_PreservesSuffix(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	modelRegistry.RegisterClient("test-request-details-gemini", "gemini", []*registry.ModelInfo{{ID: "gemini-2.5-pro", Created: now + 30}, {ID: "gemini-2.5-flash", Created: now + 25}})
	modelRegistry.RegisterClient("test-request-details-openai", "openai", []*registry.ModelInfo{{ID: "gpt-5.2", Created: now + 20}})
	modelRegistry.RegisterClient("test-request-details-claude", "claude", []*registry.ModelInfo{{ID: "claude-sonnet-4-5", Created: now + 5}})

	clientIDs := []string{"test-request-details-gemini", "test-request-details-openai", "test-request-details-claude"}
	for _, clientID := range clientIDs {
		id := clientID
		t.Cleanup(func() { modelRegistry.UnregisterClient(id) })
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	tests := []struct {
		name          string
		inputModel    string
		wantProviders []string
		wantModel     string
		wantErr       bool
	}{
		{name: "numeric suffix preserved", inputModel: "gemini-2.5-pro(8192)", wantProviders: []string{"gemini"}, wantModel: "gemini-2.5-pro(8192)", wantErr: false},
		{name: "level suffix preserved", inputModel: "gpt-5.2(high)", wantProviders: []string{"openai"}, wantModel: "gpt-5.2(high)", wantErr: false},
		{name: "no suffix unchanged", inputModel: "claude-sonnet-4-5", wantProviders: []string{"claude"}, wantModel: "claude-sonnet-4-5", wantErr: false},
		{name: "unknown model with suffix", inputModel: "unknown-model(8192)", wantProviders: nil, wantModel: "", wantErr: true},
		{name: "auto suffix resolved", inputModel: "auto(high)", wantProviders: []string{"gemini"}, wantModel: "gemini-2.5-pro(high)", wantErr: false},
		{name: "special suffix none preserved", inputModel: "gemini-2.5-flash(none)", wantProviders: []string{"gemini"}, wantModel: "gemini-2.5-flash(none)", wantErr: false},
		{name: "special suffix auto preserved", inputModel: "claude-sonnet-4-5(auto)", wantProviders: []string{"claude"}, wantModel: "claude-sonnet-4-5(auto)", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, model, errMsg := handler.getRequestDetails(tt.inputModel)
			if (errMsg != nil) != tt.wantErr {
				t.Fatalf("getRequestDetails() error = %v, wantErr %v", errMsg, tt.wantErr)
			}
			if errMsg != nil {
				return
			}
			if !reflect.DeepEqual(providers, tt.wantProviders) {
				t.Fatalf("getRequestDetails() providers = %v, want %v", providers, tt.wantProviders)
			}
			if model != tt.wantModel {
				t.Fatalf("getRequestDetails() model = %v, want %v", model, tt.wantModel)
			}
		})
	}
}

func TestGetRequestDetails_UsesOAuthAliasForProviderLookup(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const authID = "test-request-details-github-copilot"
	const realModel = "gemini-3-pro-preview"
	const aliasModel = "gemini-3.1-pro-co"

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"github-copilot": {{Name: realModel, Alias: aliasModel, Fork: true}},
	})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "github-copilot", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	modelRegistry.RegisterClient(authID, "github-copilot", []*registry.ModelInfo{{ID: realModel}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails(aliasModel)
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %v", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"github-copilot"}) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, []string{"github-copilot"})
	}
	if model != aliasModel {
		t.Fatalf("getRequestDetails() model = %q, want %q", model, aliasModel)
	}
}

func TestGetRequestDetails_UsesClaudeOAuthAliasWithoutRegisteredAliasModel(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const authID = "test-request-details-claude-oauth"
	const realModel = "claude-sonnet-4-6"
	const aliasModel = "sonnet"

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"claude": {{Name: realModel, Alias: aliasModel, Fork: true}},
	})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	modelRegistry.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: realModel}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails(aliasModel)
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %v", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"claude"}) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, []string{"claude"})
	}
	if model != aliasModel {
		t.Fatalf("getRequestDetails() model = %q, want %q", model, aliasModel)
	}
}

func TestGetRequestDetails_UsesClaudeOAuthAliasWithoutAnyRegisteredModels(t *testing.T) {
	const realModel = "claude-sonnet-4-6"
	const aliasModel = "sonnet"

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"claude": {{Name: realModel, Alias: aliasModel, Fork: true}},
	})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "test-request-details-claude-oauth-no-registry-models", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails(aliasModel)
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %v", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"claude"}) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, []string{"claude"})
	}
	if model != aliasModel {
		t.Fatalf("getRequestDetails() model = %q, want %q", model, aliasModel)
	}
}

func TestAttachUnknownProviderUpstreamHint_UsesConfiguredClaudeBaseURL(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "claude-oauth-upstream-hint", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "base_url": "https://proxy.example.com/anthropic"}}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("handler", handler)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx.Request = req.WithContext(context.WithValue(req.Context(), "gin", ctx))

	attachUnknownProviderUpstreamHint(ctx.Request.Context(), "sonnet", "sonnet")

	v, exists := ctx.Get("API_REQUEST_SUMMARY")
	if !exists {
		t.Fatal("expected API_REQUEST_SUMMARY to be set")
	}
	summary, ok := v.(map[string]string)
	if !ok {
		t.Fatalf("API_REQUEST_SUMMARY type = %T, want map[string]string", v)
	}
	if got := summary["url"]; got != "https://proxy.example.com/anthropic/v1/messages?beta=true" {
		t.Fatalf("summary url = %q, want %q", got, "https://proxy.example.com/anthropic/v1/messages?beta=true")
	}
}

func TestGetRequestDetails_DoesNotUseOAuthAliasWhenProviderFamilyMismatches(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"claude": {{Name: "gemini-2.5-pro", Alias: "sonnet", Fork: true}},
	})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "test-request-details-claude-oauth-mismatch", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails("sonnet")
	if errMsg == nil {
		t.Fatalf("expected getRequestDetails() to fail, got providers=%v model=%q", providers, model)
	}
	if len(providers) != 0 || model != "" {
		t.Fatalf("expected no providers/model on mismatch, got providers=%v model=%q", providers, model)
	}
	if !strings.Contains(errMsg.Error.Error(), "sonnet") {
		t.Fatalf("expected mismatch error to mention model, got %v", errMsg.Error)
	}
}

func TestGetRequestDetails_FallbackModelResolution(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	modelRegistry.RegisterClient("test-fallback-claude", "claude", []*registry.ModelInfo{{ID: "sonnet", Created: now + 10}})
	t.Cleanup(func() { modelRegistry.UnregisterClient("test-fallback-claude") })

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetFallbackModels(map[string]string{"gpt-5.5": "sonnet"})
	manager.SetFallbackChain([]string{"haiku", "free-code"}, 5)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "test-fallback-claude", Provider: "claude", Status: coreauth.StatusActive}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails("gpt-5.5")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %v (expected fallback to resolve)", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"claude"}) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, []string{"claude"})
	}
	if model != "sonnet" {
		t.Fatalf("getRequestDetails() model = %q, want %q", model, "sonnet")
	}
}

func TestGetRequestDetails_FallbackChainResolution(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	modelRegistry.RegisterClient("test-fallback-chain-gemini", "gemini", []*registry.ModelInfo{{ID: "haiku", Created: now + 10}})
	t.Cleanup(func() { modelRegistry.UnregisterClient("test-fallback-chain-gemini") })

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetFallbackChain([]string{"haiku", "free-code"}, 5)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "test-fallback-chain-gemini", Provider: "gemini", Status: coreauth.StatusActive}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails("unknown-model")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %v (expected fallback chain to resolve)", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"gemini"}) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, []string{"gemini"})
	}
	if model != "haiku" {
		t.Fatalf("getRequestDetails() model = %q, want %q", model, "haiku")
	}
}

func TestGetRequestDetails_NoFallbackConfigReturnsError(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails("gpt-5.5")
	if errMsg == nil {
		t.Fatalf("expected error for unknown model with no fallback, got providers=%v model=%q", providers, model)
	}
}

func TestGetRequestDetails_ImageModelReturns503(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	_, _, errMsg := handler.getRequestDetails("gpt-image-2")
	if errMsg == nil {
		t.Fatalf("expected error for gpt-image-2, got nil")
	}
	if errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: got %d want %d", errMsg.StatusCode, http.StatusServiceUnavailable)
	}
	if errMsg.Error == nil {
		t.Fatalf("expected error message, got nil")
	}
	msg := errMsg.Error.Error()
	if !strings.Contains(msg, "/v1/images/generations") || !strings.Contains(msg, "/v1/images/edits") {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestGetRequestDetails_UnknownModelFallsBackToConfiguredFallback(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const authID = "test-request-details-fallback-gemini"
	const fallbackModel = "gemini-2.5-pro"
	now := time.Now().Unix()

	modelRegistry.RegisterClient(authID, "gemini", []*registry.ModelInfo{{ID: fallbackModel, Created: now}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:     authID,
		Label:  "test-fallback",
		Provider: "gemini",
		Status: coreauth.StatusActive,
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	manager.SetFallbackModels(map[string]string{
		"gpt-5.5": fallbackModel,
	})
	manager.SetFallbackChain([]string{fallbackModel, "lower-coding", "free-code"}, 3)

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	providers, model, errMsg := handler.getRequestDetails("gpt-5.5")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %v", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"gemini"}) {
		t.Fatalf("getRequestDetails() providers = %v, want [gemini]", providers)
	}
	if model != fallbackModel {
		t.Fatalf("getRequestDetails() model = %q, want %q", model, fallbackModel)
	}
}

func TestGetRequestDetails_UnknownModelWithoutFallbackReturnsError(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	manager.SetFallbackModels(map[string]string{})
	manager.SetFallbackChain(nil, 3)

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	providers, model, errMsg := handler.getRequestDetails("totally-unknown-model-xyz")
	if errMsg == nil {
		t.Fatalf("expected error, got providers=%v model=%q", providers, model)
	}
	if len(providers) != 0 || model != "" {
		t.Fatalf("expected empty result, got providers=%v model=%q", providers, model)
	}
	if !strings.Contains(errMsg.Error.Error(), "totally-unknown-model-xyz") {
		t.Fatalf("expected error to mention model, got %v", errMsg.Error)
	}
}

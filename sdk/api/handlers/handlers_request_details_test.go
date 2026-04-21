package handlers

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestGetRequestDetails_PreservesSuffix(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	modelRegistry.RegisterClient("test-request-details-gemini", "gemini", []*registry.ModelInfo{
		{ID: "gemini-2.5-pro", Created: now + 30},
		{ID: "gemini-2.5-flash", Created: now + 25},
	})
	modelRegistry.RegisterClient("test-request-details-openai", "openai", []*registry.ModelInfo{
		{ID: "gpt-5.2", Created: now + 20},
	})
	modelRegistry.RegisterClient("test-request-details-claude", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4-5", Created: now + 5},
	})

	// Ensure cleanup of all test registrations.
	clientIDs := []string{
		"test-request-details-gemini",
		"test-request-details-openai",
		"test-request-details-claude",
	}
	for _, clientID := range clientIDs {
		id := clientID
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(id)
		})
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	tests := []struct {
		name          string
		inputModel    string
		wantProviders []string
		wantModel     string
		wantErr       bool
	}{
		{
			name:          "numeric suffix preserved",
			inputModel:    "gemini-2.5-pro(8192)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(8192)",
			wantErr:       false,
		},
		{
			name:          "level suffix preserved",
			inputModel:    "gpt-5.2(high)",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5.2(high)",
			wantErr:       false,
		},
		{
			name:          "no suffix unchanged",
			inputModel:    "claude-sonnet-4-5",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5",
			wantErr:       false,
		},
		{
			name:          "unknown model with suffix",
			inputModel:    "unknown-model(8192)",
			wantProviders: nil,
			wantModel:     "",
			wantErr:       true,
		},
		{
			name:          "auto suffix resolved",
			inputModel:    "auto(high)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(high)",
			wantErr:       false,
		},
		{
			name:          "special suffix none preserved",
			inputModel:    "gemini-2.5-flash(none)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-flash(none)",
			wantErr:       false,
		},
		{
			name:          "special suffix auto preserved",
			inputModel:    "claude-sonnet-4-5(auto)",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5(auto)",
			wantErr:       false,
		},
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
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         authID,
		Provider:   "github-copilot",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	modelRegistry.RegisterClient(authID, "github-copilot", []*registry.ModelInfo{{ID: realModel}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(authID)
	})

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
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	modelRegistry.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: realModel}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(authID)
	})

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
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "test-request-details-claude-oauth-no-registry-models",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}); err != nil {
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
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-oauth-upstream-hint",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://proxy.example.com/anthropic",
		},
	}); err != nil {
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
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "test-request-details-claude-oauth-mismatch",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails("sonnet")
	if errMsg == nil {
		t.Fatalf("expected getRequestDetails() to fail, got providers=%v model=%q", providers, model)
	}
}

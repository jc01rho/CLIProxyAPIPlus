package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuthMimocodeRequiresWhitelist(t *testing.T) {
	registry := internalregistry.GetGlobalRegistry()
	tests := []struct {
		name   string
		models []config.MimocodeModel
		want   []string
	}{
		{name: "empty whitelist", models: nil, want: nil},
		{name: "explicit whitelist", models: []config.MimocodeModel{
			{Name: "mimo-v2.5-pro", Alias: "mimo-pro"},
			{Name: "mimo-v2.5-pro-ultraspeed"},
		}, want: []string{"mimo-pro", "mimo-v2.5-pro-ultraspeed"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			authID := "mimocode-models-" + tc.name
			registry.UnregisterClient(authID)
			t.Cleanup(func() { registry.UnregisterClient(authID) })
			service := &Service{cfg: &config.Config{MimocodeKey: []config.MimocodeKey{{APIKey: "secret", Models: tc.models}}}}
			auth := &coreauth.Auth{ID: authID, Provider: "mimocode", Status: coreauth.StatusActive, Attributes: map[string]string{
				coreauth.AttributeAPIKey:      "secret",
				coreauth.AttributeConfigIndex: "0",
				coreauth.AttributeSource:      "config:mimocode[test]",
			}}
			service.registerModelsForAuth(context.Background(), auth)
			got := registry.GetModelsForClient(authID)
			if len(got) != len(tc.want) {
				t.Fatalf("registered model count = %d, want %d: %+v", len(got), len(tc.want), got)
			}
			for _, modelID := range tc.want {
				assertRegisteredModel(t, got, modelID)
			}
		})
	}
}

func TestRegisterModelsForMimocodeAuthFileUsesExplicitMetadataWhitelist(t *testing.T) {
	registry := internalregistry.GetGlobalRegistry()
	authID := "mimocode-file-models"
	registry.UnregisterClient(authID)
	t.Cleanup(func() { registry.UnregisterClient(authID) })
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{ID: authID, Provider: "mimocode", Status: coreauth.StatusActive, Metadata: map[string]any{
		"models": []any{map[string]any{"name": "mimo-v2.5", "alias": "mimo"}},
	}}
	service.registerModelsForAuth(context.Background(), auth)
	models := registry.GetModelsForClient(authID)
	if len(models) != 1 {
		t.Fatalf("registered model count = %d, want 1", len(models))
	}
	assertRegisteredModel(t, models, "mimo")
}

func TestBuildMimocodeConfigModelsUsesStaticLimits(t *testing.T) {
	models := buildMimocodeConfigModels(&config.MimocodeKey{Models: []config.MimocodeModel{{Name: "mimo-v2.5-pro", Alias: "pro"}}})
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	if models[0].ContextLength != 1_000_000 || models[0].MaxCompletionTokens != 131072 {
		t.Fatalf("model limits = %d/%d", models[0].ContextLength, models[0].MaxCompletionTokens)
	}
	if models[0].ExecutionTarget != "mimo-v2.5-pro" {
		t.Fatalf("execution target = %q", models[0].ExecutionTarget)
	}
}

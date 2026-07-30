package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuth_ConfiguredNativeProviderModelsAppearInOpenAIList(t *testing.T) {
	service := &Service{cfg: &config.Config{
		CommandCodeKey: []config.CommandCodeKey{{
			APIKey: "commandcode-key",
			Models: []config.CommandCodeModel{{Name: "qwen3-coder", Alias: "commandcode-visible"}},
		}},
		MistralKey: []config.MistralKey{{
			APIKey: "mistral-key",
			Models: []config.MistralModel{{Name: "mistral-medium-latest", Alias: "mistral-visible"}},
		}},
	}}

	tests := []struct {
		provider string
		apiKey   string
		modelID  string
	}{
		{provider: "commandcode", apiKey: "commandcode-key", modelID: "commandcode-visible"},
		{provider: "mistral", apiKey: "mistral-key", modelID: "mistral-visible"},
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	for _, testCase := range tests {
		t.Run(testCase.provider, func(t *testing.T) {
			authID := "provider-models-" + testCase.provider
			modelRegistry.UnregisterClient(authID)
			t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

			auth := &coreauth.Auth{
				ID:       authID,
				Provider: testCase.provider,
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					coreauth.AttributeAPIKey:      testCase.apiKey,
					coreauth.AttributeConfigIndex: "0",
					coreauth.AttributeSource:      "config:" + testCase.provider + ":test",
				},
			}

			service.registerModelsForAuth(context.Background(), auth)
			assertRegisteredModel(t, modelRegistry.GetModelsForClient(authID), testCase.modelID)
			assertOpenAIModel(t, modelRegistry.GetAvailableModels("openai"), testCase.modelID)
		})
	}
}

func TestRegisterModelsForAuth_AllStaticProviderCatalogsAppearInOpenAIList(t *testing.T) {
	tests := []struct {
		provider string
		modelID  string
	}{
		{provider: "gemini-cli", modelID: internalregistry.GetGeminiCLIModels()[0].ID},
		{provider: "github-copilot", modelID: internalregistry.GetGitHubCopilotModels()[0].ID},
		{provider: "kiro", modelID: internalregistry.GetKiroModels()[0].ID},
		{provider: "kilo", modelID: internalregistry.GetKiloModels()[0].ID},
		{provider: "amazonq", modelID: internalregistry.GetAmazonQModels()[0].ID},
		{provider: "codebuddy", modelID: internalregistry.GetCodeBuddyModels()[0].ID},
		{provider: "cursor", modelID: internalregistry.GetCursorModels()[0].ID},
		{provider: "cline", modelID: internalregistry.GetClineModels()[0].ID},
	}

	service := &Service{cfg: &config.Config{}}
	modelRegistry := internalregistry.GetGlobalRegistry()
	for _, testCase := range tests {
		t.Run(testCase.provider, func(t *testing.T) {
			authID := "static-provider-models-" + testCase.provider
			modelRegistry.UnregisterClient(authID)
			t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

			auth := &coreauth.Auth{ID: authID, Provider: testCase.provider, Status: coreauth.StatusActive}
			service.registerModelsForAuth(context.Background(), auth)
			assertRegisteredModel(t, modelRegistry.GetModelsForClient(authID), testCase.modelID)
			assertOpenAIModel(t, modelRegistry.GetAvailableModels("openai"), testCase.modelID)
		})
	}
}

func assertRegisteredModel(t *testing.T, models []*internalregistry.ModelInfo, want string) {
	t.Helper()
	for _, model := range models {
		if model != nil && model.ID == want {
			return
		}
	}
	t.Fatalf("registered models do not contain %q: %+v", want, models)
}

func assertOpenAIModel(t *testing.T, models []map[string]any, want string) {
	t.Helper()
	for _, model := range models {
		if model["id"] == want {
			return
		}
	}
	t.Fatalf("/v1/models source does not contain %q: %+v", want, models)
}

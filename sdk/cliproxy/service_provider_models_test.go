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
		{provider: "mistral", modelID: internalregistry.GetMistralModels()[0].ID},
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

// TestRegisterModelsForAuth_MistralProviderWithoutExplicitModelsFallsBackToStaticCatalog
// asserts that a configured Mistral credential without an explicit `models`
// field still surfaces the static Mistral catalog through /v1/models. Before
// the fix, registerModelsForAuth dropped the client registration whenever
// buildMistralConfigModels returned nil, leaving Mistral out of the OpenAI-
// compatible model list.
func TestRegisterModelsForAuth_MistralProviderWithoutExplicitModelsFallsBackToStaticCatalog(t *testing.T) {
	static := internalregistry.GetMistralModels()
	if len(static) == 0 {
		t.Fatalf("expected static Mistral catalog to be non-empty")
	}

	service := &Service{cfg: &config.Config{
		MistralKey: []config.MistralKey{{APIKey: "mistral-key"}},
	}}

	modelRegistry := internalregistry.GetGlobalRegistry()
	authID := "static-mistral-fallback"
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "mistral",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: "mistral-key",
			coreauth.AttributeSource: "config:mistral:test",
		},
	}

	service.registerModelsForAuth(context.Background(), auth)

	registered := modelRegistry.GetModelsForClient(authID)
	if len(registered) == 0 {
		t.Fatalf("expected Mistral auth to register fallback models, got none")
	}
	for _, expected := range static {
		assertRegisteredModel(t, registered, expected.ID)
		assertOpenAIModel(t, modelRegistry.GetAvailableModels("openai"), expected.ID)
	}
}

// TestRegisterModelsForAuth_MistralProviderWithExplicitModelsUsesConfigAndIgnoresStaticFallback
// ensures that an explicit MistralKey.Models entry still takes precedence and
// the static catalog is not merged in (preserving the existing policy).
func TestRegisterModelsForAuth_MistralProviderWithExplicitModelsUsesConfigAndIgnoresStaticFallback(t *testing.T) {
	const configuredAlias = "mistral-configured-only"

	service := &Service{cfg: &config.Config{
		MistralKey: []config.MistralKey{{
			APIKey: "mistral-key",
			Models: []config.MistralModel{{Name: "mistral-large-latest", Alias: configuredAlias}},
		}},
	}}

	modelRegistry := internalregistry.GetGlobalRegistry()
	authID := "static-mistral-configured-only"
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "mistral",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: "mistral-key",
			coreauth.AttributeSource: "config:mistral:test",
		},
	}

	service.registerModelsForAuth(context.Background(), auth)

	registered := modelRegistry.GetModelsForClient(authID)
	assertRegisteredModel(t, registered, configuredAlias)
	for _, model := range registered {
		if model == nil {
			continue
		}
		for _, staticModel := range internalregistry.GetMistralModels() {
			if staticModel == nil {
				continue
			}
			if model.ID == staticModel.ID {
				t.Fatalf("static Mistral catalog must not leak when explicit models are configured: %q", model.ID)
			}
		}
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

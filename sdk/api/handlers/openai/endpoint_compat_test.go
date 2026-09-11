package openai

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestResolveEndpointOverride_StripsThinkingSuffix(t *testing.T) {
	const clientID = "test-endpoint-compat-suffix"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "github-copilot", []*registry.ModelInfo{
		{
			ID:                 "test-gemini-chat-only",
			SupportedEndpoints: []string{openAIChatEndpoint},
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	override, ok := resolveEndpointOverride("test-gemini-chat-only(high)", openAIResponsesEndpoint)
	if !ok {
		t.Fatalf("expected endpoint override to be resolved")
	}
	if override != openAIChatEndpoint {
		t.Fatalf("override endpoint = %q, want %q", override, openAIChatEndpoint)
	}
}

// TestResolveEndpointOverride_DevinModelsAreChatOnly pins the Devin routing
// contract: Devin speaks a Connect chat RPC, so a /v1/responses request must be
// converted to /chat/completions instead of being streamed raw, which would end
// the Responses SSE stream without a terminal response event.
func TestResolveEndpointOverride_DevinModelsAreChatOnly(t *testing.T) {
	const clientID = "test-endpoint-compat-devin"
	reg := registry.GetGlobalRegistry()
	devinModels := registry.GetDevinModels()
	if len(devinModels) == 0 {
		t.Fatal("GetDevinModels() returned no models")
	}
	for _, model := range devinModels {
		if !endpointListContains(model.SupportedEndpoints, openAIChatEndpoint) {
			t.Fatalf("devin model %q must declare %q, got %v", model.ID, openAIChatEndpoint, model.SupportedEndpoints)
		}
		if endpointListContains(model.SupportedEndpoints, openAIResponsesEndpoint) {
			t.Fatalf("devin model %q must not declare %q", model.ID, openAIResponsesEndpoint)
		}
	}

	reg.RegisterClient(clientID, "devin", devinModels)
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	override, ok := resolveEndpointOverride("swe-2-high", openAIResponsesEndpoint)
	if !ok {
		t.Fatal("expected /v1/responses for swe-2-high to be overridden to chat completions")
	}
	if override != openAIChatEndpoint {
		t.Fatalf("override endpoint = %q, want %q", override, openAIChatEndpoint)
	}

	if _, ok = resolveEndpointOverride("swe-2-high", openAIChatEndpoint); ok {
		t.Fatal("chat completions requests for swe-2-high must not be overridden")
	}
}

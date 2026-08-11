package management

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestGetStaticModelDefinitionsPrefersRuntimeProviderModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const clientID = "test-cline-dynamic-model-definitions"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "cline", []*registry.ModelInfo{
		{
			ID:          "openai/gpt-live-catalog",
			DisplayName: "GPT Live Catalog",
			Type:        "cline",
			OwnedBy:     "cline",
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channel", Value: "cline"}}

	(&Handler{}).GetStaticModelDefinitions(ctx)

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Models []registry.ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Models) != 1 {
		t.Fatalf("models length = %d, want 1: %#v", len(response.Models), response.Models)
	}
	if got := response.Models[0].ID; got != "openai/gpt-live-catalog" {
		t.Fatalf("model id = %q, want runtime model", got)
	}
	if got := response.Models[0].DisplayName; got != "GPT Live Catalog" {
		t.Fatalf("display_name = %q, want runtime display name", got)
	}
}

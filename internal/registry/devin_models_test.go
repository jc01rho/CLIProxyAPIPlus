package registry

import (
	"testing"
)

func TestValidateDevinModelsJSON(t *testing.T) {
	t.Run("valid envelope devin", func(t *testing.T) {
		data := []byte(`{
			"devin": [
				{
					"id": "swe-2",
					"display_name": "SWE-2",
					"owned_by": "cognition",
					"context_length": 262000
				}
			]
		}`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "swe-2" {
			t.Fatalf("unexpected models: %+v", models)
		}
		if models[0].Type != "devin" {
			t.Errorf("expected type 'devin', got %q", models[0].Type)
		}
	})

	t.Run("valid envelope models", func(t *testing.T) {
		data := []byte(`{
			"models": [
				{
					"id": "glm-5-2",
					"display_name": "GLM-5.2"
				}
			]
		}`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "glm-5-2" {
			t.Fatalf("unexpected models: %+v", models)
		}
	})

	t.Run("valid direct array", func(t *testing.T) {
		data := []byte(`[
			{
				"id": "deepseek-v4-flash",
				"display_name": "DeepSeek V4 Flash"
			}
		]`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "deepseek-v4-flash" {
			t.Fatalf("unexpected models: %+v", models)
		}
	})

	t.Run("clean id without devin prefix kept bare", func(t *testing.T) {
		data := []byte(`{
			"devin": [
				{
					"id": "swe-2",
					"display_name": "SWE-2"
				}
			]
		}`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "swe-2" {
			t.Fatalf("expected bare id swe-2 preserved, got: %q", models[0].ID)
		}
	})

	t.Run("empty payload", func(t *testing.T) {
		_, err := ValidateDevinModelsJSON([]byte(`   `))
		if err == nil {
			t.Fatal("expected error on empty payload, got nil")
		}
	})

	t.Run("empty model id", func(t *testing.T) {
		data := []byte(`{"devin": [{"id": "", "display_name": "No ID"}]}`)
		_, err := ValidateDevinModelsJSON(data)
		if err == nil {
			t.Fatal("expected error on empty model id, got nil")
		}
	})

	t.Run("duplicate model id", func(t *testing.T) {
		data := []byte(`{"devin": [{"id": "swe-2"}, {"id": "swe-2"}]}`)
		_, err := ValidateDevinModelsJSON(data)
		if err == nil {
			t.Fatal("expected error on duplicate model id, got nil")
		}
	})
}

func TestEmbeddedDevinModelsLoadedOnStartup(t *testing.T) {
	models := GetDevinModels()
	if len(models) < 30 {
		t.Fatalf("expected at least 30 embedded Devin models, got %d", len(models))
	}

	foundMap := make(map[string]bool)
	for _, m := range models {
		foundMap[m.ID] = true
	}

	expectedIDs := []string{
		"swe-2",
		"glm-5-2",
		"glm-5-3",
		"deepseek-v4-flash",
		"deepseek-v4-1-flash",
		"gemini-3-8-flash",
		"grok-4-6",
		"claude-fable-5-1",
		"gpt-6-astra",
	}

	for _, id := range expectedIDs {
		if !foundMap[id] {
			t.Errorf("expected embedded catalog to contain %q", id)
		}
	}
}

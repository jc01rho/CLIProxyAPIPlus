package registry

import "testing"

func TestGetMimocodeModelsCurrentChatCatalogOnly(t *testing.T) {
	models := GetMimocodeModels()
	want := map[string]bool{
		"mimo-v2.6-pro":            false,
		"mimo-v2.6-flash":          false,
		"mimo-v2.6-pro-ultraspeed": false,
		"mimo-v2.5-pro":            false,
		"mimo-v2.5":                false,
		"mimo-v2.5-pro-ultraspeed": false,
	}
	if len(models) != len(want) {
		t.Fatalf("model count = %d, want %d", len(models), len(want))
	}
	for _, model := range models {
		if _, ok := want[model.ID]; !ok {
			t.Fatalf("unexpected Mimocode model %q", model.ID)
		}
		want[model.ID] = true
		if model.ContextLength != 1_000_000 || model.MaxCompletionTokens != 131072 {
			t.Fatalf("%s limits = %d/%d", model.ID, model.ContextLength, model.MaxCompletionTokens)
		}
	}
	for modelID, found := range want {
		if !found {
			t.Fatalf("missing model %q", modelID)
		}
	}
}

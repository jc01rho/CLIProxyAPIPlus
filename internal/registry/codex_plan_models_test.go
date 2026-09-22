package registry

import "testing"

func TestCodexPaidPlanCatalogsExposeGPT6SolAndLuna(t *testing.T) {
	plans := map[string][]*ModelInfo{
		"team": GetCodexTeamModels(),
		"plus": GetCodexPlusModels(),
		"pro":  GetCodexProModels(),
	}
	for plan, models := range plans {
		t.Run(plan, func(t *testing.T) {
			for _, modelID := range []string{"gpt-6-sol", "gpt-6-luna"} {
				if !containsRegistryModel(models, modelID) {
					t.Errorf("%s catalog is missing %s", plan, modelID)
				}
			}
		})
	}
}

func containsRegistryModel(models []*ModelInfo, modelID string) bool {
	for _, model := range models {
		if model != nil && model.ID == modelID {
			return true
		}
	}
	return false
}
